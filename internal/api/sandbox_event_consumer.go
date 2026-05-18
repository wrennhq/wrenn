package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/events"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

const (
	unifiedEventStream    = "wrenn:events"
	reconcilerConsumerGrp = "wrenn-sandbox-reconciler-v1"
	reconcilerConsumer    = "cp-0"
)

// SandboxEventConsumer reads capsule lifecycle events from the unified Redis
// stream and drives DB state reconciliation. Uses an independent consumer
// group so its cursor is separate from the channels dispatcher.
type SandboxEventConsumer struct {
	rdb   *redis.Client
	db    *db.Queries
	audit *audit.AuditLogger
}

// NewSandboxEventConsumer creates a consumer.
func NewSandboxEventConsumer(rdb *redis.Client, queries *db.Queries, al *audit.AuditLogger) *SandboxEventConsumer {
	return &SandboxEventConsumer{rdb: rdb, db: queries, audit: al}
}

// Start launches the consumer goroutine. Reads from "$" so prior history
// is not replayed.
func (c *SandboxEventConsumer) Start(ctx context.Context) {
	go c.run(ctx)
}

func (c *SandboxEventConsumer) run(ctx context.Context) {
	err := c.rdb.XGroupCreateMkStream(ctx, unifiedEventStream, reconcilerConsumerGrp, "$").Err()
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
			Group:    reconcilerConsumerGrp,
			Consumer: reconcilerConsumer,
			Streams:  []string{unifiedEventStream, ">"},
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
	defer func() {
		ackCtx, ackCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ackCancel()
		if err := c.rdb.XAck(ackCtx, unifiedEventStream, reconcilerConsumerGrp, msg.ID).Err(); err != nil {
			slog.Warn("sandbox event consumer: xack failed", "id", msg.ID, "error", err)
		}
	}()

	payload, ok := msg.Values["payload"].(string)
	if !ok {
		slog.Warn("sandbox event consumer: message missing payload", "id", msg.ID)
		return
	}

	var event events.Event
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		slog.Warn("sandbox event consumer: failed to unmarshal event", "id", msg.ID, "error", err)
		return
	}

	// Only capsule.* events drive sandbox reconciliation.
	if !strings.HasPrefix(event.Event, "capsule.") || event.Event == events.CapsuleStateChanged {
		return
	}
	// Only system-actor events represent host-side state we need to reflect
	// in the DB; user-actor events are already mirrored by the handler that
	// produced them.
	if event.Actor.Type != events.ActorSystem {
		// Exception: handlers publish capsule.create with user actor before
		// the host has reported back. Those are owned by the service goroutine.
		return
	}

	sandboxID, err := id.ParseSandboxID(event.Resource.ID)
	if err != nil {
		slog.Warn("sandbox event consumer: invalid sandbox ID", "sandbox_id", event.Resource.ID, "error", err)
		return
	}

	switch event.Event {
	case events.CapsuleCreate:
		if event.Outcome == events.OutcomeSuccess {
			c.handleStarted(ctx, sandboxID, event, "starting")
		} else {
			c.handleFailed(ctx, sandboxID)
		}
	case events.CapsuleResume:
		if event.Outcome == events.OutcomeSuccess {
			c.handleStarted(ctx, sandboxID, event, "resuming")
		} else {
			c.handleFailed(ctx, sandboxID)
		}
	case events.CapsulePause:
		if event.Outcome == events.OutcomeSuccess {
			c.handleAutoPaused(ctx, sandboxID)
		}
	case events.CapsuleDestroy:
		if event.Outcome == events.OutcomeSuccess {
			c.handleStopped(ctx, sandboxID)
		}
	}
}

// handleStarted is a fallback writer for capsule.create.success and
// capsule.resume.success. The background goroutine in SandboxService is the
// primary writer; this only succeeds if the goroutine's conditional update
// was missed.
func (c *SandboxEventConsumer) handleStarted(ctx context.Context, sandboxID pgtype.UUID, event events.Event, fromStatus string) {
	hostIP := event.Metadata["host_ip"]
	now := time.Now()
	if _, err := c.db.UpdateSandboxRunningIf(ctx, db.UpdateSandboxRunningIfParams{
		ID:     sandboxID,
		Status: fromStatus,
		HostIp: hostIP,
		StartedAt: pgtype.Timestamptz{
			Time:  now,
			Valid: true,
		},
	}); err != nil {
		return
	}
}

func (c *SandboxEventConsumer) handleAutoPaused(ctx context.Context, sandboxID pgtype.UUID) {
	for _, fromStatus := range []string{"running", "pausing"} {
		if _, err := c.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: fromStatus, Status_2: "paused",
		}); err == nil {
			slog.Debug("sandbox event consumer: auto-paused fallback applied", "sandbox_id", id.FormatSandboxID(sandboxID), "from", fromStatus)
			return
		}
	}
}

func (c *SandboxEventConsumer) handleStopped(ctx context.Context, sandboxID pgtype.UUID) {
	// stopping → stopped (CP-initiated destroy completed). No audit row here;
	// the handler that issued the destroy already wrote one.
	if _, err := c.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
		ID:       sandboxID,
		Status:   "stopping",
		Status_2: "stopped",
	}); err == nil {
		return
	}
	// running → stopped (autonomous destroy, e.g. TTL destroy fallback).
	if _, err := c.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
		ID:       sandboxID,
		Status:   "running",
		Status_2: "stopped",
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("sandbox event consumer: failed to update sandbox to stopped", "sandbox_id", id.FormatSandboxID(sandboxID), "error", err)
	}
}

// handleFailed marks a sandbox as "error" when a verb event reports failure.
func (c *SandboxEventConsumer) handleFailed(ctx context.Context, sandboxID pgtype.UUID) {
	for _, fromStatus := range []string{"running", "starting", "pausing", "resuming"} {
		if _, err := c.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: fromStatus, Status_2: "error",
		}); err == nil {
			return
		}
	}
}
