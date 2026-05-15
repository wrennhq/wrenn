package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

type sandboxEventHandler struct {
	db  *db.Queries
	rdb *redis.Client
}

func newSandboxEventHandler(queries *db.Queries, rdb *redis.Client) *sandboxEventHandler {
	return &sandboxEventHandler{db: queries, rdb: rdb}
}

type sandboxEventRequest struct {
	Event     string `json:"event"`
	SandboxID string `json:"sandbox_id"`
	HostID    string `json:"host_id"`
	Timestamp int64  `json:"timestamp"`
}

// Handle receives lifecycle event callbacks from host agents and publishes
// them to the internal Redis stream for the SandboxEventConsumer to process.
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

	// Validate that the calling host matches the host_id in the payload.
	hc := auth.MustHostFromContext(r.Context())
	callerHostID := id.FormatHostID(hc.HostID)
	if callerHostID != req.HostID {
		writeError(w, http.StatusForbidden, "forbidden", "host_id does not match authenticated host")
		return
	}

	if req.Timestamp == 0 {
		req.Timestamp = time.Now().Unix()
	}

	PublishSandboxEvent(r.Context(), h.rdb, SandboxEvent{
		Event:     req.Event,
		SandboxID: req.SandboxID,
		HostID:    req.HostID,
		Timestamp: req.Timestamp,
	})

	w.WriteHeader(http.StatusNoContent)
}
