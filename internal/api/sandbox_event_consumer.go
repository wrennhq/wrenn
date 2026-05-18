package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

const (
	sandboxEventStream   = "wrenn:sandbox-events"
	sandboxEventGroup    = "wrenn-sandbox-events-v1"
	sandboxEventConsumer = "cp-0"
)

// SandboxEvent is the canonical event payload published to the Redis stream
// by both the CP background goroutines (for explicit lifecycle ops) and
// the agent callback endpoint (for autonomous events like auto-pause).
//
// TeamID is populated by publishers that already have it in hand (the CP
// service layer); consumers that need it can either trust the field or
// re-derive it from a sandbox DB lookup.
type SandboxEvent struct {
	Event     string            `json:"event"`
	SandboxID string            `json:"sandbox_id"`
	TeamID    string            `json:"team_id,omitempty"`
	HostID    string            `json:"host_id"`
	HostIP    string            `json:"host_ip,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Error     string            `json:"error,omitempty"`
	Timestamp int64             `json:"timestamp"`
}

// Sandbox event type constants. Convention:
//   - sandbox.started: emitted ONLY after a successful initial Create.
//   - sandbox.resumed: emitted after a successful Resume, INCLUDING any
//     future autonomous recovery path (so the SSE mapping in server.go
//     stays correct without distinguishing recovery from explicit resume).
//   - sandbox.pause_failed / sandbox.resume_failed: terminal failure of
//     the background lifecycle op; the in-DB status is rolled back by the
//     publishing goroutine and the consumer surfaces an SSE error event.
const (
	SandboxEventStarted      = "sandbox.started"
	SandboxEventPaused       = "sandbox.paused"
	SandboxEventResumed      = "sandbox.resumed"
	SandboxEventStopped      = "sandbox.stopped"
	SandboxEventFailed       = "sandbox.failed"
	SandboxEventError        = "sandbox.error"
	SandboxEventAutoPaused   = "sandbox.auto_paused"
	SandboxEventPauseFailed  = "sandbox.pause_failed"
	SandboxEventResumeFailed = "sandbox.resume_failed"
)

// SandboxEventConsumer reads sandbox lifecycle events from the Redis stream
// and updates database state accordingly. It follows the same XREADGROUP
// pattern as pkg/channels/dispatcher.go.
type SandboxEventConsumer struct {
	rdb   *redis.Client
	db    *db.Queries
	audit *audit.AuditLogger
}

// NewSandboxEventConsumer creates a consumer.
func NewSandboxEventConsumer(rdb *redis.Client, queries *db.Queries, al *audit.AuditLogger) *SandboxEventConsumer {
	return &SandboxEventConsumer{rdb: rdb, db: queries, audit: al}
}

// Start launches the consumer goroutine.
func (c *SandboxEventConsumer) Start(ctx context.Context) {
	go c.run(ctx)
}

func (c *SandboxEventConsumer) run(ctx context.Context) {
	err := c.rdb.XGroupCreateMkStream(ctx, sandboxEventStream, sandboxEventGroup, "$").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		slog.Error("sandbox event consumer: failed to create consumer group", "error", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		streams, err := c.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    sandboxEventGroup,
			Consumer: sandboxEventConsumer,
			Streams:  []string{sandboxEventStream, ">"},
			Count:    10,
			Block:    5 * time.Second,
		}).Result()

		if err != nil {
			if err == redis.Nil || ctx.Err() != nil {
				continue
			}
			slog.Warn("sandbox event consumer: xreadgroup error", "error", err)
			time.Sleep(1 * time.Second)
			continue
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				c.handleMessage(ctx, msg)
			}
		}
	}
}

func (c *SandboxEventConsumer) handleMessage(ctx context.Context, msg redis.XMessage) {
	// Use a non-cancellable context for XAck so shutdown doesn't leave
	// messages permanently stuck in the pending entries list.
	defer func() {
		ackCtx, ackCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ackCancel()
		if err := c.rdb.XAck(ackCtx, sandboxEventStream, sandboxEventGroup, msg.ID).Err(); err != nil {
			slog.Warn("sandbox event consumer: xack failed", "id", msg.ID, "error", err)
		}
	}()

	payload, ok := msg.Values["payload"].(string)
	if !ok {
		slog.Warn("sandbox event consumer: message missing payload", "id", msg.ID)
		return
	}

	var event SandboxEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		slog.Warn("sandbox event consumer: failed to unmarshal event", "id", msg.ID, "error", err)
		return
	}

	sandboxID, err := id.ParseSandboxID(event.SandboxID)
	if err != nil {
		slog.Warn("sandbox event consumer: invalid sandbox ID", "sandbox_id", event.SandboxID, "error", err)
		return
	}

	switch event.Event {
	case SandboxEventStarted:
		c.handleStarted(ctx, sandboxID, event, "starting")
	case SandboxEventResumed:
		c.handleStarted(ctx, sandboxID, event, "resuming")
	case SandboxEventPaused:
		c.handlePaused(ctx, sandboxID, event)
	case SandboxEventStopped:
		c.handleStopped(ctx, sandboxID, event)
	case SandboxEventFailed, SandboxEventError:
		c.handleFailed(ctx, sandboxID)
	case SandboxEventAutoPaused:
		c.handleAutoPaused(ctx, sandboxID, event)
	case SandboxEventPauseFailed:
		// Background goroutine in SandboxService already rolled the DB
		// state back from "pausing" → "running" (or "error"); this is just
		// the audit-trail ack.
		slog.Info("sandbox pause failed", "sandbox_id", event.SandboxID, "error", event.Error)
	case SandboxEventResumeFailed:
		slog.Info("sandbox resume failed", "sandbox_id", event.SandboxID, "error", event.Error)
	default:
		slog.Warn("sandbox event consumer: unknown event type", "event", event.Event)
	}
}

// handleStarted is a fallback writer for sandbox.started and sandbox.resumed
// events. The background goroutine in SandboxService is the primary writer;
// this only succeeds if the goroutine's conditional update was missed.
func (c *SandboxEventConsumer) handleStarted(ctx context.Context, sandboxID pgtype.UUID, event SandboxEvent, fromStatus string) {
	now := time.Now()
	if _, err := c.db.UpdateSandboxRunningIf(ctx, db.UpdateSandboxRunningIfParams{
		ID:     sandboxID,
		Status: fromStatus,
		HostIp: event.HostIP,
		StartedAt: pgtype.Timestamptz{
			Time:  now,
			Valid: true,
		},
	}); err != nil {
		return
	}

	if len(event.Metadata) > 0 {
		metaJSON, _ := json.Marshal(event.Metadata)
		_ = c.db.UpdateSandboxMetadata(ctx, db.UpdateSandboxMetadataParams{
			ID:       sandboxID,
			Metadata: metaJSON,
		})
	}
}

func (c *SandboxEventConsumer) handlePaused(ctx context.Context, sandboxID pgtype.UUID, event SandboxEvent) {
	if _, err := c.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
		ID: sandboxID, Status: "pausing", Status_2: "paused",
	}); err != nil {
		return
	}
	slog.Debug("sandbox event consumer: paused fallback applied", "sandbox_id", event.SandboxID)
}

func (c *SandboxEventConsumer) handleStopped(ctx context.Context, sandboxID pgtype.UUID, event SandboxEvent) {
	// Try stopping → stopped (CP-initiated destroy completed).
	if _, err := c.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
		ID:       sandboxID,
		Status:   "stopping",
		Status_2: "stopped",
	}); err == nil {
		return
	}
	// Try running → stopped (autonomous destroy, e.g. TTL auto-destroy).
	if _, err := c.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
		ID:       sandboxID,
		Status:   "running",
		Status_2: "stopped",
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("sandbox event consumer: failed to update sandbox to stopped", "sandbox_id", event.SandboxID, "error", err)
	}
}

// handleFailed marks a sandbox as "error" when the host agent reports a crash
// or the CP's background goroutine publishes a failure. Uses conditional update
// to avoid clobbering concurrent operations.
func (c *SandboxEventConsumer) handleFailed(ctx context.Context, sandboxID pgtype.UUID) {
	// Try each possible pre-failure state until one matches.
	for _, fromStatus := range []string{"running", "starting", "pausing", "resuming"} {
		if _, err := c.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: fromStatus, Status_2: "error",
		}); err == nil {
			return
		}
	}
}

func (c *SandboxEventConsumer) handleAutoPaused(ctx context.Context, sandboxID pgtype.UUID, event SandboxEvent) {
	// Try each plausible pre-pause state. Shutdown-time PauseAll can race a
	// user-initiated pause that already moved the DB to "pausing"; without
	// the second attempt that row would stay stuck until the HostMonitor
	// transient-grace period elapses (2 minutes).
	for _, fromStatus := range []string{"running", "pausing"} {
		if _, err := c.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: fromStatus, Status_2: "paused",
		}); err == nil {
			slog.Debug("sandbox event consumer: auto-paused fallback applied", "sandbox_id", event.SandboxID, "from", fromStatus)
			return
		}
	}
}

// PublishSandboxEvent writes a sandbox lifecycle event to the Redis stream.
// Used by both the SandboxService background goroutines and the callback endpoint.
func PublishSandboxEvent(ctx context.Context, rdb *redis.Client, event SandboxEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		slog.Warn("sandbox event: failed to marshal", "event", event.Event, "error", err)
		return
	}

	if err := rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: sandboxEventStream,
		MaxLen: 50000,
		Approx: true,
		Values: map[string]any{
			"payload": string(payload),
		},
	}).Err(); err != nil {
		slog.Warn("sandbox event: failed to publish", "event", event.Event, "error", err)
	}
}
