package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
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
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}

	if req.Event == "" || req.SandboxID == "" || req.HostID == "" {
		writeErr(w, r, apperr.ValidationFailed.Msg("The event, sandbox_id, and host_id fields are required."))
		return
	}

	hc := auth.MustHostFromContext(r.Context())
	callerHostID := id.FormatHostID(hc.HostID)
	if callerHostID != req.HostID {
		writeErr(w, r, apperr.Forbidden.Msg("The host_id does not match the authenticated host."))
		return
	}

	sandboxUUID, parseErr := id.ParseSandboxID(req.SandboxID)
	if parseErr != nil {
		writeErr(w, r, apperr.ValidationFailed.Msg("Invalid sandbox_id."))
		return
	}

	sb, sbErr := h.db.GetSandbox(r.Context(), sandboxUUID)
	if sbErr != nil {
		// Unknown sandbox — accept and ignore so the host agent does not retry a
		// callback for a row we no longer have (e.g. already destroyed).
		slog.Warn("sandbox event callback: unknown sandbox", "sandbox_id", req.SandboxID)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// The host_id check above only proves the caller is *a* valid host, not the
	// owner of this sandbox. Without this a host could report state for any
	// team's sandbox by ID — flipping its status and spoofing lifecycle events
	// into the victim team's audit log, channels, and dashboard.
	if sb.HostID != hc.HostID {
		writeErr(w, r, apperr.Forbidden.Msg("The sandbox is not managed by the authenticated host."))
		return
	}

	if req.Timestamp == 0 {
		req.Timestamp = time.Now().Unix()
	}

	evt, ok := h.translate(req, sb)
	if !ok {
		// Unknown event type — log and accept so the host agent doesn't retry.
		slog.Warn("sandbox event callback: untranslatable event", "event", req.Event, "sandbox_id", req.SandboxID)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	h.eventPub.Publish(r.Context(), evt)
	w.WriteHeader(http.StatusNoContent)
}

// translate converts a raw host-agent callback into the canonical event. The
// sandbox row is resolved and ownership-checked by the caller (Handle) and
// passed in, so this is a pure mapping with no further DB access.
// For failure events without an in-flight verb (e.g. sandbox.failed), the
// sandbox's current status is consulted to pick the appropriate verb.
func (h *sandboxEventHandler) translate(req sandboxEventRequest, sb db.Sandbox) (events.Event, bool) {
	base := events.Event{
		Timestamp: time.Unix(req.Timestamp, 0).UTC().Format(time.RFC3339),
		TeamID:    id.FormatTeamID(sb.TeamID),
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
		// Pick a verb based on the sandbox's current status.
		base.Event = verbForFailure(sb.Status)
		base.Outcome = events.OutcomeError
		base.Error = req.Error
		base.Metadata = map[string]string{"reason": "host_failure"}
	default:
		return events.Event{}, false
	}
	return base, true
}

func verbForFailure(status string) string {
	switch status {
	case "starting":
		return events.CapsuleCreate
	case "resuming":
		return events.CapsuleResume
	case "pausing":
		return events.CapsulePause
	case "snapshotting":
		// A snapshot pauses then resumes the VM; a host-side failure leaves the
		// sandbox errored, not destroyed. Route through CapsuleCreate so the
		// consumer's handleFailed marks it "error" rather than removing the row.
		return events.CapsuleCreate
	default:
		return events.CapsuleDestroy
	}
}
