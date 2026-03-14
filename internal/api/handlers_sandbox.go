package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/validate"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
)

type sandboxHandler struct {
	db    *db.Queries
	agent hostagentv1connect.HostAgentServiceClient
}

func newSandboxHandler(db *db.Queries, agent hostagentv1connect.HostAgentServiceClient) *sandboxHandler {
	return &sandboxHandler{db: db, agent: agent}
}

type createSandboxRequest struct {
	Template   string `json:"template"`
	VCPUs      int32  `json:"vcpus"`
	MemoryMB   int32  `json:"memory_mb"`
	TimeoutSec int32  `json:"timeout_sec"`
}

type sandboxResponse struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	Template     string  `json:"template"`
	VCPUs        int32   `json:"vcpus"`
	MemoryMB     int32   `json:"memory_mb"`
	TimeoutSec   int32   `json:"timeout_sec"`
	GuestIP      string  `json:"guest_ip,omitempty"`
	HostIP       string  `json:"host_ip,omitempty"`
	CreatedAt    string  `json:"created_at"`
	StartedAt    *string `json:"started_at,omitempty"`
	LastActiveAt *string `json:"last_active_at,omitempty"`
	LastUpdated  string  `json:"last_updated"`
}

func sandboxToResponse(sb db.Sandbox) sandboxResponse {
	resp := sandboxResponse{
		ID:         sb.ID,
		Status:     sb.Status,
		Template:   sb.Template,
		VCPUs:      sb.Vcpus,
		MemoryMB:   sb.MemoryMb,
		TimeoutSec: sb.TimeoutSec,
		GuestIP:    sb.GuestIp,
		HostIP:     sb.HostIp,
	}
	if sb.CreatedAt.Valid {
		resp.CreatedAt = sb.CreatedAt.Time.Format(time.RFC3339)
	}
	if sb.StartedAt.Valid {
		s := sb.StartedAt.Time.Format(time.RFC3339)
		resp.StartedAt = &s
	}
	if sb.LastActiveAt.Valid {
		s := sb.LastActiveAt.Time.Format(time.RFC3339)
		resp.LastActiveAt = &s
	}
	if sb.LastUpdated.Valid {
		resp.LastUpdated = sb.LastUpdated.Time.Format(time.RFC3339)
	}
	return resp
}

// Create handles POST /v1/sandboxes.
func (h *sandboxHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if req.Template == "" {
		req.Template = "minimal"
	}
	if err := validate.SafeName(req.Template); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("invalid template name: %s", err))
		return
	}
	if req.VCPUs <= 0 {
		req.VCPUs = 1
	}
	if req.MemoryMB <= 0 {
		req.MemoryMB = 512
	}
	// timeout_sec = 0 means no auto-pause; only set if explicitly requested.

	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	// If the template is a snapshot, use its baked-in vcpus/memory
	// (they cannot be changed since the VM state is frozen).
	if tmpl, err := h.db.GetTemplateByTeam(ctx, db.GetTemplateByTeamParams{Name: req.Template, TeamID: ac.TeamID}); err == nil && tmpl.Type == "snapshot" {
		if tmpl.Vcpus.Valid {
			req.VCPUs = tmpl.Vcpus.Int32
		}
		if tmpl.MemoryMb.Valid {
			req.MemoryMB = tmpl.MemoryMb.Int32
		}
	}
	sandboxID := id.NewSandboxID()

	// Insert pending record.
	_, err := h.db.InsertSandbox(ctx, db.InsertSandboxParams{
		ID:         sandboxID,
		TeamID:     ac.TeamID,
		HostID:     "default",
		Template:   req.Template,
		Status:     "pending",
		Vcpus:      req.VCPUs,
		MemoryMb:   req.MemoryMB,
		TimeoutSec: req.TimeoutSec,
	})
	if err != nil {
		slog.Error("failed to insert sandbox", "error", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to create sandbox record")
		return
	}

	// Call host agent to create the sandbox.
	resp, err := h.agent.CreateSandbox(ctx, connect.NewRequest(&pb.CreateSandboxRequest{
		SandboxId:  sandboxID,
		Template:   req.Template,
		Vcpus:      req.VCPUs,
		MemoryMb:   req.MemoryMB,
		TimeoutSec: req.TimeoutSec,
	}))
	if err != nil {
		if _, dbErr := h.db.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
			ID: sandboxID, Status: "error",
		}); dbErr != nil {
			slog.Warn("failed to update sandbox status to error", "id", sandboxID, "error", dbErr)
		}
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	// Update to running.
	now := time.Now()
	sb, err := h.db.UpdateSandboxRunning(ctx, db.UpdateSandboxRunningParams{
		ID:      sandboxID,
		HostIp:  resp.Msg.HostIp,
		GuestIp: "",
		StartedAt: pgtype.Timestamptz{
			Time:  now,
			Valid: true,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to update sandbox status")
		return
	}

	writeJSON(w, http.StatusCreated, sandboxToResponse(sb))
}

// List handles GET /v1/sandboxes.
func (h *sandboxHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	sandboxes, err := h.db.ListSandboxesByTeam(r.Context(), ac.TeamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list sandboxes")
		return
	}

	resp := make([]sandboxResponse, len(sandboxes))
	for i, sb := range sandboxes {
		resp[i] = sandboxToResponse(sb)
	}

	writeJSON(w, http.StatusOK, resp)
}

// Get handles GET /v1/sandboxes/{id}.
func (h *sandboxHandler) Get(w http.ResponseWriter, r *http.Request) {
	sandboxID := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sb, err := h.db.GetSandboxByTeam(r.Context(), db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	writeJSON(w, http.StatusOK, sandboxToResponse(sb))
}

// Pause handles POST /v1/sandboxes/{id}/pause.
// Pause = snapshot + destroy. The sandbox is frozen to disk and all running
// resources are released. It can be resumed later.
func (h *sandboxHandler) Pause(w http.ResponseWriter, r *http.Request) {
	sandboxID := chi.URLParam(r, "id")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sb.Status != "running" {
		writeError(w, http.StatusConflict, "invalid_state", "sandbox is not running")
		return
	}

	if _, err := h.agent.PauseSandbox(ctx, connect.NewRequest(&pb.PauseSandboxRequest{
		SandboxId: sandboxID,
	})); err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	sb, err = h.db.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
		ID: sandboxID, Status: "paused",
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to update status")
		return
	}

	writeJSON(w, http.StatusOK, sandboxToResponse(sb))
}

// Resume handles POST /v1/sandboxes/{id}/resume.
// Resume restores a paused sandbox from snapshot using UFFD lazy memory loading.
func (h *sandboxHandler) Resume(w http.ResponseWriter, r *http.Request) {
	sandboxID := chi.URLParam(r, "id")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sb.Status != "paused" {
		writeError(w, http.StatusConflict, "invalid_state", "sandbox is not paused")
		return
	}

	resp, err := h.agent.ResumeSandbox(ctx, connect.NewRequest(&pb.ResumeSandboxRequest{
		SandboxId:  sandboxID,
		TimeoutSec: sb.TimeoutSec,
	}))
	if err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	now := time.Now()
	sb, err = h.db.UpdateSandboxRunning(ctx, db.UpdateSandboxRunningParams{
		ID:      sandboxID,
		HostIp:  resp.Msg.HostIp,
		GuestIp: "",
		StartedAt: pgtype.Timestamptz{
			Time:  now,
			Valid: true,
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to update status")
		return
	}

	writeJSON(w, http.StatusOK, sandboxToResponse(sb))
}

// Ping handles POST /v1/sandboxes/{id}/ping.
// Resets the inactivity timer for a running sandbox.
func (h *sandboxHandler) Ping(w http.ResponseWriter, r *http.Request) {
	sandboxID := chi.URLParam(r, "id")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sb.Status != "running" {
		writeError(w, http.StatusConflict, "invalid_state", "sandbox is not running")
		return
	}

	if _, err := h.agent.PingSandbox(ctx, connect.NewRequest(&pb.PingSandboxRequest{
		SandboxId: sandboxID,
	})); err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	if err := h.db.UpdateLastActive(ctx, db.UpdateLastActiveParams{
		ID: sandboxID,
		LastActiveAt: pgtype.Timestamptz{
			Time:  time.Now(),
			Valid: true,
		},
	}); err != nil {
		slog.Warn("ping: failed to update last_active_at in DB", "sandbox_id", sandboxID, "error", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

// Destroy handles DELETE /v1/sandboxes/{id}.
func (h *sandboxHandler) Destroy(w http.ResponseWriter, r *http.Request) {
	sandboxID := chi.URLParam(r, "id")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	_, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	// Best-effort destroy on host agent — sandbox may already be gone (TTL reap).
	if _, err := h.agent.DestroySandbox(ctx, connect.NewRequest(&pb.DestroySandboxRequest{
		SandboxId: sandboxID,
	})); err != nil {
		slog.Warn("destroy: agent RPC failed (sandbox may already be gone)", "sandbox_id", sandboxID, "error", err)
	}

	if _, err := h.db.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
		ID: sandboxID, Status: "stopped",
	}); err != nil {
		slog.Error("destroy: failed to update sandbox status in DB", "sandbox_id", sandboxID, "error", err)
	}

	w.WriteHeader(http.StatusNoContent)
}
