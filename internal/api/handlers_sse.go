package api

import (
	"fmt"
	"net/http"
	"time"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

const sseKeepaliveInterval = 30 * time.Second

type sseHandler struct {
	broker *SSEBroker
}

func newSSEHandler(broker *SSEBroker) *sseHandler {
	return &sseHandler{broker: broker}
}

// Stream handles GET /v1/events/stream. Authentication is performed by
// upstream middleware: browser clients use the wrenn_sid cookie (which the
// EventSource API forwards automatically); SDK clients use X-API-Key.
func (h *sseHandler) Stream(w http.ResponseWriter, r *http.Request) {
	ac, ok := auth.FromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "session cookie or X-API-Key required")
		return
	}
	h.serveSSE(w, r, id.FormatTeamID(ac.TeamID), false)
}

// AdminStream handles GET /v1/admin/events/stream. Upstream middleware
// must enforce session auth + requireAdmin before reaching this handler.
func (h *sseHandler) AdminStream(w http.ResponseWriter, r *http.Request) {
	ac, ok := auth.FromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "admin session required")
		return
	}
	h.serveSSE(w, r, id.FormatTeamID(ac.TeamID), true)
}

func (h *sseHandler) serveSSE(w http.ResponseWriter, r *http.Request, teamID string, isAdmin bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	subID, ch := h.broker.Subscribe(teamID, isAdmin)
	defer h.broker.Unsubscribe(subID)

	fmt.Fprintf(w, "event: connected\ndata: {\"message\":\"connected\"}\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.EventType, msg.Data)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}
