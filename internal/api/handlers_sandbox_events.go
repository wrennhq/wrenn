package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/channels"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/events"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

type sandboxEventHandler struct {
	db       *db.Queries
	eventPub *channels.Publisher
}

func newSandboxEventHandler(queries *db.Queries, eventPub *channels.Publisher) *sandboxEventHandler {
	return &sandboxEventHandler{db: queries, eventPub: eventPub}
}

type sandboxEventRequest struct {
	Event     string `json:"event"`
	SandboxID string `json:"sandbox_id"`
	HostID    string `json:"host_id"`
	HostIP    string `json:"host_ip,omitempty"`
	Error     string `json:"error,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// Handle receives lifecycle event callbacks from host agents, translates the
// raw host event into the canonical events.Event taxonomy, and publishes once
// to the unified Redis stream. The SandboxEventConsumer (independent
// consumer group) drives DB reconciliation; the channels dispatcher delivers
// to subscribed channels; the SSE relay mirrors via Pub/Sub.
func (h *sandboxEventHandler) Handle(w http.ResponseWriter, r *http.Request) {
	var req sandboxEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if req.Event == "" || req.SandboxID == "" || req.HostID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "event, sandbox_id, and host_id are required")
		return
	}

	hc := auth.MustHostFromContext(r.Context())
	callerHostID := id.FormatHostID(hc.HostID)
	if callerHostID != req.HostID {
		writeError(w, http.StatusForbidden, "forbidden", "host_id does not match authenticated host")
		return
	}

	if req.Timestamp == 0 {
		req.Timestamp = time.Now().Unix()
	}

	evt, ok := h.translate(r.Context(), req)
	if !ok {
		// Unknown event type — log and accept so the host agent doesn't retry.
		slog.Warn("sandbox event callback: untranslatable event", "event", req.Event, "sandbox_id", req.SandboxID)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.eventPub.Publish(r.Context(), evt)
	w.WriteHeader(http.StatusNoContent)
}

// translate converts a raw host-agent callback into the canonical event.
// For failure events without an in-flight verb (e.g. sandbox.failed), the
// current DB status is consulted to pick the appropriate verb.
func (h *sandboxEventHandler) translate(ctx context.Context, req sandboxEventRequest) (events.Event, bool) {
	sandboxUUID, parseErr := id.ParseSandboxID(req.SandboxID)
	if parseErr != nil {
		return events.Event{}, false
	}

	var teamID pgtype.UUID
	if sb, dbErr := h.db.GetSandbox(ctx, sandboxUUID); dbErr == nil {
		teamID = sb.TeamID
	}

	base := events.Event{
		Timestamp: time.Unix(req.Timestamp, 0).UTC().Format(time.RFC3339),
		TeamID:    id.FormatTeamID(teamID),
		Actor:     events.SystemActor(),
		Resource:  events.Resource{ID: req.SandboxID, Type: "sandbox"},
	}

	switch req.Event {
	case "sandbox.started":
		meta := map[string]string{}
		if req.HostIP != "" {
			meta["host_ip"] = req.HostIP
		}
		meta["host_id"] = req.HostID
		base.Event = events.CapsuleCreate
		base.Outcome = events.OutcomeSuccess
		base.Metadata = meta
	case "sandbox.resumed":
		meta := map[string]string{"host_id": req.HostID}
		if req.HostIP != "" {
			meta["host_ip"] = req.HostIP
		}
		base.Event = events.CapsuleResume
		base.Outcome = events.OutcomeSuccess
		base.Metadata = meta
	case "sandbox.paused":
		base.Event = events.CapsulePause
		base.Outcome = events.OutcomeSuccess
	case "sandbox.auto_paused":
		base.Event = events.CapsulePause
		base.Outcome = events.OutcomeSuccess
		base.Metadata = map[string]string{"reason": "ttl_expired"}
	case "sandbox.stopped":
		base.Event = events.CapsuleDestroy
		base.Outcome = events.OutcomeSuccess
	case "sandbox.pause_failed":
		base.Event = events.CapsulePause
		base.Outcome = events.OutcomeError
		base.Error = req.Error
		base.Metadata = map[string]string{"reason": "host_failure"}
	case "sandbox.resume_failed":
		base.Event = events.CapsuleResume
		base.Outcome = events.OutcomeError
		base.Error = req.Error
		base.Metadata = map[string]string{"reason": "host_failure"}
	case "sandbox.failed", "sandbox.error":
		// Pick a verb based on the sandbox's current DB status.
		verb := h.verbForFailure(ctx, sandboxUUID)
		base.Event = verb
		base.Outcome = events.OutcomeError
		base.Error = req.Error
		base.Metadata = map[string]string{"reason": "host_failure"}
	default:
		return events.Event{}, false
	}
	return base, true
}

func (h *sandboxEventHandler) verbForFailure(ctx context.Context, sandboxID pgtype.UUID) string {
	sb, err := h.db.GetSandbox(ctx, sandboxID)
	if err != nil {
		return events.CapsuleDestroy
	}
	switch sb.Status {
	case "starting":
		return events.CapsuleCreate
	case "resuming":
		return events.CapsuleResume
	case "pausing":
		return events.CapsulePause
	default:
		return events.CapsuleDestroy
	}
}
