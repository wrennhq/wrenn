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
	"git.omukk.dev/wrenn/wrenn/pkg/cpextension"
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
	hooks []cpextension.SandboxEventHook
}

// NewSandboxEventConsumer creates a consumer.
func NewSandboxEventConsumer(rdb *redis.Client, queries *db.Queries, al *audit.AuditLogger, hooks []cpextension.SandboxEventHook) *SandboxEventConsumer {
	return &SandboxEventConsumer{rdb: rdb, db: queries, audit: al, hooks: hooks}
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
	ack := true
	defer func() {
		if !ack {
			return
		}
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
			c.handleFailed(ctx, sandboxID, event)
		}
	case events.CapsuleResume:
		if event.Outcome == events.OutcomeSuccess {
			c.handleStarted(ctx, sandboxID, event, "resuming")
		} else {
			c.handleFailed(ctx, sandboxID, event)
		}
	case events.CapsulePause:
		if event.Outcome == events.OutcomeSuccess {
			c.handleAutoPaused(ctx, sandboxID, event)
		}
	case events.CapsuleDestroy:
		if event.Outcome == events.OutcomeSuccess {
			c.handleStopped(ctx, sandboxID)
		}
	}

	// Dispatch to extension hooks (cloud billing, audit shipping, etc.). Any
	// hook error suppresses the ack so the message will be redelivered. Hooks
	// MUST be idempotent — duplicate deliveries are expected on transient
	// failures.
	if len(c.hooks) > 0 && event.Outcome == events.OutcomeSuccess {
		if verb, ok := canonicalSandboxVerb(event.Event); ok {
			teamID, _ := id.ParseTeamID(event.TeamID)
			meta := map[string]any{}
			for k, v := range event.Metadata {
				meta[k] = v
			}
			ev := cpextension.SandboxEvent{
				SandboxID:  sandboxID,
				TeamID:     teamID,
				Type:       verb,
				OccurredAt: parseEventTimestamp(event.Timestamp),
				Metadata:   meta,
			}
			for _, h := range c.hooks {
				if err := h.OnSandboxEvent(ctx, ev); err != nil {
					slog.Warn("sandbox event hook failed; leaving message un-acked", "id", msg.ID, "event", event.Event, "error", err)
					ack = false
					return
				}
			}
		}
	}
}

func canonicalSandboxVerb(event string) (string, bool) {
	switch event {
	case events.CapsuleCreate:
		return "created", true
	case events.CapsuleResume:
		return "resumed", true
	case events.CapsulePause:
		return "paused", true
	case events.CapsuleDestroy:
		return "destroyed", true
	}
	return "", false
}

func parseEventTimestamp(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t
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

// handleAutoPaused reflects an autonomous (TTL reaper / shutdown) pause in the
// DB and writes the audit row for it. The audit write happens only when the
// status flip actually applied, so a stream redelivery does not double-count,
// and so the HostMonitor host_state_sync fallback (which audits the
// callback-lost case) stays mutually exclusive with this path.
//
// Uses audit.Log (row only) — NOT LogSandboxAutoPause, which republishes a
// CapsulePause/system event that would loop straight back into this consumer.
func (c *SandboxEventConsumer) handleAutoPaused(ctx context.Context, sandboxID pgtype.UUID, event events.Event) {
	for _, fromStatus := range []string{"running", "pausing"} {
		if _, err := c.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: fromStatus, Status_2: "paused",
		}); err == nil {
			slog.Debug("sandbox event consumer: auto-paused applied", "sandbox_id", id.FormatSandboxID(sandboxID), "from", fromStatus)
			reason := event.Metadata["reason"]
			if reason == "" {
				reason = "ttl_expired"
			}
			teamID, _ := id.ParseTeamID(event.TeamID)
			c.audit.Log(ctx, audit.Entry{
				TeamID:       teamID,
				ActorType:    "system",
				ResourceType: "sandbox",
				ResourceID:   id.FormatSandboxID(sandboxID),
				Action:       "pause",
				Scope:        "team",
				Status:       "info",
				Metadata:     map[string]any{"reason": reason},
			})
			return
		}
	}
}

// detachVolumes frees any volumes still attached to a sandbox that has reached
// a terminal state. Idempotent (only 'attached' rows change) and keyed on
// sandbox_id, so it runs safely from every terminal path — the explicit destroy
// flow, the TTL-reaper fallback, and the crash/failure path — not just
// SandboxService.Destroy.
func (c *SandboxEventConsumer) detachVolumes(ctx context.Context, sandboxID pgtype.UUID) {
	if err := c.db.DetachVolumesBySandbox(ctx, sandboxID); err != nil {
		slog.Warn("sandbox event consumer: failed to detach volumes", "sandbox_id", id.FormatSandboxID(sandboxID), "error", err)
	}
}

func (c *SandboxEventConsumer) handleStopped(ctx context.Context, sandboxID pgtype.UUID) {
	c.detachVolumes(ctx, sandboxID)

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

// handleFailed marks a sandbox as "error" when a verb event reports failure
// and writes a system audit row. The DB update is idempotent — the
// SandboxService background goroutine usually wrote "error" already on the
// fast-fail path, which settles in seconds and so never reaches the
// HostMonitor's transient-timeout reconciliation.
//
// audit.Log writes the row only — it does NOT republish an event, which would
// loop back into this consumer. Do not switch to LogSandboxCreateSystem here.
func (c *SandboxEventConsumer) handleFailed(ctx context.Context, sandboxID pgtype.UUID, event events.Event) {
	c.detachVolumes(ctx, sandboxID)

	for _, fromStatus := range []string{"running", "starting", "pausing", "resuming", "snapshotting"} {
		if _, err := c.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: fromStatus, Status_2: "error",
		}); err == nil {
			break
		}
	}

	// The HostMonitor transient-timeout reconciler emits failure events via
	// LogSandboxCreateSystem / LogSandboxResumeSystem, which already write
	// their own audit row before publishing — auditing again here would
	// double-count. Those helpers publish with reason="transient_timeout";
	// the un-audited fast-fail (createInBackground) and host-callback paths
	// do not, so only they need a row written here.
	if event.Metadata["reason"] == "transient_timeout" {
		return
	}

	// The lifecycle op that failed is recorded as a distinct "error" action
	// (attributed to system) rather than a second "create"/"resume" row, so the
	// audit log reads "a capsule encountered an error" instead of masquerading
	// as a duplicate, system-attributed creation. The user-attributed create row
	// was already written at request-accept time by the handler.
	phase := "create"
	if event.Event == events.CapsuleResume {
		phase = "resume"
	}
	reason := event.Metadata["reason"]
	if reason == "" {
		reason = phase + "_failed"
	}
	meta := map[string]any{"reason": reason, "phase": phase}
	if event.Error != "" {
		meta["error"] = event.Error
	}
	teamID, _ := id.ParseTeamID(event.TeamID)
	c.audit.Log(ctx, audit.Entry{
		TeamID:       teamID,
		ActorType:    "system",
		ResourceType: "sandbox",
		ResourceID:   id.FormatSandboxID(sandboxID),
		Action:       "error",
		Scope:        "team",
		Status:       "error",
		Metadata:     meta,
	})
}
