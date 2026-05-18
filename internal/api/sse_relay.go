package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/events"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

const ssePubSubChannel = "wrenn:sse"

// sseEventPayload is the JSON envelope sent to SSE clients.
type sseEventPayload struct {
	Event     string           `json:"event"`
	Timestamp string           `json:"timestamp"`
	TeamID    string           `json:"team_id"`
	Actor     events.Actor     `json:"actor"`
	Resource  events.Resource  `json:"resource"`
	Sandbox   *sandboxResponse `json:"sandbox,omitempty"`
}

// SSERelay subscribes to the Redis Pub/Sub channel and dispatches hydrated
// events to the in-process broker. One instance per CP process.
type SSERelay struct {
	rdb    *redis.Client
	db     *db.Queries
	broker *SSEBroker
}

// NewSSERelay constructs the relay.
func NewSSERelay(rdb *redis.Client, queries *db.Queries, broker *SSEBroker) *SSERelay {
	return &SSERelay{rdb: rdb, db: queries, broker: broker}
}

// Start launches the Pub/Sub subscription goroutine. Returns when ctx is cancelled.
func (r *SSERelay) Start(ctx context.Context) {
	go r.run(ctx)
}

func (r *SSERelay) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		r.subscribe(ctx)
		// Backoff before reconnecting.
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (r *SSERelay) subscribe(ctx context.Context) {
	pubsub := r.rdb.Subscribe(ctx, ssePubSubChannel)
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			r.handleMessage(ctx, msg)
		}
	}
}

func (r *SSERelay) handleMessage(ctx context.Context, msg *redis.Message) {
	var event events.Event
	if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
		slog.Warn("sse relay: failed to unmarshal event", "error", err)
		return
	}

	payload := sseEventPayload{
		Event:     event.Event,
		Timestamp: event.Timestamp,
		TeamID:    event.TeamID,
		Actor:     event.Actor,
		Resource:  event.Resource,
	}

	// Hydrate sandbox state for capsule events.
	if isCapsuleEvent(event.Event) {
		sb, err := r.hydrateSandbox(ctx, event.Resource.ID)
		if err != nil {
			slog.Debug("sse relay: sandbox hydration failed (may be deleted)", "sandbox_id", event.Resource.ID, "error", err)
		} else {
			payload.Sandbox = sb
		}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("sse relay: failed to marshal payload", "error", err)
		return
	}

	r.broker.Dispatch(event.Event, event.TeamID, data)
}

func (r *SSERelay) hydrateSandbox(ctx context.Context, sandboxIDStr string) (*sandboxResponse, error) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		return nil, err
	}

	sb, err := r.db.GetSandbox(queryCtx, sandboxID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	resp := sandboxToResponse(sb)
	return &resp, nil
}

func isCapsuleEvent(eventType string) bool {
	switch eventType {
	case events.CapsuleCreated, events.CapsuleRunning, events.CapsulePaused, events.CapsuleDestroyed:
		return true
	}
	return false
}
