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
	Event     string            `json:"event"`
	Outcome   events.Outcome    `json:"outcome,omitempty"`
	Timestamp string            `json:"timestamp"`
	TeamID    string            `json:"team_id"`
	Actor     events.Actor      `json:"actor"`
	Resource  events.Resource   `json:"resource"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Error     string            `json:"error,omitempty"`
	Sandbox   *sandboxResponse  `json:"sandbox,omitempty"`
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
		Outcome:   event.Outcome,
		Timestamp: event.Timestamp,
		TeamID:    event.TeamID,
		Actor:     event.Actor,
		Resource:  event.Resource,
		Metadata:  event.Metadata,
		Error:     event.Error,
	}

	// Hydrate sandbox state for capsule events.
	if isCapsuleEvent(event.Event) {
		sb, err := r.hydrateSandbox(ctx, event.Resource.ID)
		if err != nil {
			slog.Debug("sse relay: sandbox hydration failed (may be deleted)", "sandbox_id", event.Resource.ID, "error", err)
		} else {
			// Override the hydrated status with the status implied by the event
			// verb. Autonomous transitions (e.g. TTL auto-pause) flip the DB row
			// in a separate stream consumer that races this Pub/Sub read, so the
			// hydrated row may still carry the pre-transition status. The event
			// itself is authoritative for the resulting state.
			if status, ok := impliedSandboxStatus(event); ok {
				sb.Status = status
			}
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

// impliedSandboxStatus maps a successful capsule lifecycle event to the
// sandbox status it results in. Used to override a hydrated DB row that may
// still carry the pre-transition status because the reconciliation consumer
// that flips it races this Pub/Sub read. Returns false for events with no
// single deterministic resulting status (failures, destroy, state_changed).
func impliedSandboxStatus(event events.Event) (string, bool) {
	if event.Outcome != events.OutcomeSuccess {
		return "", false
	}
	// Only system-initiated completion events (host callbacks, TTL reaper) imply
	// a terminal status. User/API-key create/resume/pause events are published at
	// request-accept time — while the sandbox is still transient (starting /
	// resuming / pausing) — so their hydrated DB row is authoritative and must
	// not be overridden. Overriding them masks the transient state on the
	// dashboard (a launching capsule jumps straight to "running").
	if event.Actor.Type != events.ActorSystem {
		return "", false
	}
	switch event.Event {
	case events.CapsulePause:
		return "paused", true
	case events.CapsuleResume, events.CapsuleCreate:
		return "running", true
	default:
		return "", false
	}
}

func isCapsuleEvent(eventType string) bool {
	switch eventType {
	case events.CapsuleCreate, events.CapsulePause, events.CapsuleResume, events.CapsuleDestroy, events.CapsuleStateChanged:
		return true
	}
	return false
}
