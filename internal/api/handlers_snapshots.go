package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
)

type snapshotHandler struct {
	db    *db.Queries
	agent hostagentv1connect.HostAgentServiceClient
}

func newSnapshotHandler(db *db.Queries, agent hostagentv1connect.HostAgentServiceClient) *snapshotHandler {
	return &snapshotHandler{db: db, agent: agent}
}

type createSnapshotRequest struct {
	SandboxID string `json:"sandbox_id"`
	Name      string `json:"name"`
}

type snapshotResponse struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	VCPUs     *int32 `json:"vcpus,omitempty"`
	MemoryMB  *int32 `json:"memory_mb,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
}

func templateToResponse(t db.Template) snapshotResponse {
	resp := snapshotResponse{
		Name:      t.Name,
		Type:      t.Type,
		SizeBytes: t.SizeBytes,
	}
	if t.Vcpus.Valid {
		resp.VCPUs = &t.Vcpus.Int32
	}
	if t.MemoryMb.Valid {
		resp.MemoryMB = &t.MemoryMb.Int32
	}
	if t.CreatedAt.Valid {
		resp.CreatedAt = t.CreatedAt.Time.Format(time.RFC3339)
	}
	return resp
}

// Create handles POST /v1/snapshots.
func (h *snapshotHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if req.SandboxID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "sandbox_id is required")
		return
	}

	if req.Name == "" {
		req.Name = id.NewSnapshotName()
	}

	ctx := r.Context()
	overwrite := r.URL.Query().Get("overwrite") == "true"

	// Check if name already exists.
	if _, err := h.db.GetTemplate(ctx, req.Name); err == nil {
		if !overwrite {
			writeError(w, http.StatusConflict, "already_exists", "snapshot name already exists; use ?overwrite=true to replace")
			return
		}
		// Delete existing template record and files.
		h.db.DeleteTemplate(ctx, req.Name)
	}

	// Verify sandbox exists and is running or paused.
	sb, err := h.db.GetSandbox(ctx, req.SandboxID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sb.Status != "running" && sb.Status != "paused" {
		writeError(w, http.StatusConflict, "invalid_state", "sandbox must be running or paused")
		return
	}

	// Call host agent to create snapshot. If running, the agent pauses it first.
	// The sandbox remains paused after this call.
	resp, err := h.agent.CreateSnapshot(ctx, connect.NewRequest(&pb.CreateSnapshotRequest{
		SandboxId: req.SandboxID,
		Name:      req.Name,
	}))
	if err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	// Mark sandbox as paused (if it was running, it got paused by the snapshot).
	if sb.Status != "paused" {
		if _, err := h.db.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
			ID: req.SandboxID, Status: "paused",
		}); err != nil {
			slog.Error("failed to update sandbox status after snapshot", "sandbox_id", req.SandboxID, "error", err)
		}
	}

	// Insert template record.
	tmpl, err := h.db.InsertTemplate(ctx, db.InsertTemplateParams{
		Name:      req.Name,
		Type:      "snapshot",
		Vcpus:     pgtype.Int4{Int32: sb.Vcpus, Valid: true},
		MemoryMb:  pgtype.Int4{Int32: sb.MemoryMb, Valid: true},
		SizeBytes: resp.Msg.SizeBytes,
	})
	if err != nil {
		slog.Error("failed to insert template record", "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "db_error", "snapshot created but failed to record in database")
		return
	}

	writeJSON(w, http.StatusCreated, templateToResponse(tmpl))
}

// List handles GET /v1/snapshots.
func (h *snapshotHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	typeFilter := r.URL.Query().Get("type")

	var templates []db.Template
	var err error
	if typeFilter != "" {
		templates, err = h.db.ListTemplatesByType(ctx, typeFilter)
	} else {
		templates, err = h.db.ListTemplates(ctx)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list templates")
		return
	}

	resp := make([]snapshotResponse, len(templates))
	for i, t := range templates {
		resp[i] = templateToResponse(t)
	}

	writeJSON(w, http.StatusOK, resp)
}

// Delete handles DELETE /v1/snapshots/{name}.
func (h *snapshotHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ctx := r.Context()

	if _, err := h.db.GetTemplate(ctx, name); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "template not found")
		return
	}

	// Delete files on host agent.
	if _, err := h.agent.DeleteSnapshot(ctx, connect.NewRequest(&pb.DeleteSnapshotRequest{
		Name: name,
	})); err != nil {
		slog.Warn("delete snapshot: agent RPC failed", "name", name, "error", err)
	}

	if err := h.db.DeleteTemplate(ctx, name); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to delete template record")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
