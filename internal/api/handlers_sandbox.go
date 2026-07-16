package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
)

type sandboxHandler struct {
	svc   *service.SandboxService
	audit *audit.AuditLogger
}

func newSandboxHandler(svc *service.SandboxService, al *audit.AuditLogger) *sandboxHandler {
	return &sandboxHandler{svc: svc, audit: al}
}

type createSandboxRequest struct {
	Template   string            `json:"template"`
	VCPUs      int32             `json:"vcpus"`
	MemoryMB   int32             `json:"memory_mb"`
	TimeoutSec int32             `json:"timeout_sec"`
	Metadata   map[string]string `json:"metadata"`
}

type sandboxResponse struct {
	ID           string            `json:"id"`
	Status       string            `json:"status"`
	Template     string            `json:"template"`
	VCPUs        int32             `json:"vcpus"`
	MemoryMB     int32             `json:"memory_mb"`
	TimeoutSec   int32             `json:"timeout_sec"`
	DiskSizeMB   int32             `json:"disk_size_mb"`
	DiskUsedMB   *int64            `json:"disk_used_mb,omitempty"`
	CreatedAt    string            `json:"created_at"`
	StartedAt    *string           `json:"started_at,omitempty"`
	LastActiveAt *string           `json:"last_active_at,omitempty"`
	LastUpdated  string            `json:"last_updated"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

func sandboxToResponse(sb db.Sandbox) sandboxResponse {
	resp := sandboxResponse{
		ID:         id.FormatSandboxID(sb.ID),
		Status:     sb.Status,
		Template:   sb.Template,
		VCPUs:      sb.Vcpus,
		MemoryMB:   sb.MemoryMb,
		TimeoutSec: sb.TimeoutSec,
		DiskSizeMB: sb.DiskSizeMb,
	}
	if len(sb.Metadata) > 0 {
		var meta map[string]string
		if err := json.Unmarshal(sb.Metadata, &meta); err == nil && len(meta) > 0 {
			resp.Metadata = meta
		}
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

// Create handles POST /v1/capsules.
func (h *sandboxHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}

	ac := auth.MustFromContext(r.Context())
	if !ac.TeamID.Valid {
		writeErr(w, r, apperr.Forbidden.Msg("No active team context; re-authenticate."))
		return
	}

	sb, err := h.svc.Create(r.Context(), service.SandboxCreateParams{
		TeamID:     ac.TeamID,
		Template:   req.Template,
		VCPUs:      req.VCPUs,
		MemoryMB:   req.MemoryMB,
		TimeoutSec: req.TimeoutSec,
		Metadata:   req.Metadata,
	})
	h.audit.LogSandboxCreate(r.Context(), ac, sb.ID, req.Template, err)
	if err != nil {
		if sb.ID.Valid {
			h.audit.LogSandboxDestroySystem(r.Context(), ac.TeamID, sb.ID, "cleanup_after_create_error", nil)
		}
		writeErr(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, sandboxToResponse(sb))
}

// List handles GET /v1/capsules.
func (h *sandboxHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	sandboxes, err := h.svc.List(r.Context(), ac.TeamID)
	if err != nil {
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	resp := make([]sandboxResponse, len(sandboxes))
	for i, sb := range sandboxes {
		resp[i] = sandboxToResponse(sb)
	}

	writeJSON(w, http.StatusOK, resp)
}

// Get handles GET /v1/capsules/{id}.
func (h *sandboxHandler) Get(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid sandbox ID."))
		return
	}

	sb, err := h.svc.Get(r.Context(), sandboxID, ac.TeamID)
	if err != nil {
		writeErr(w, r, apperr.SandboxNotFound.Wrap(err))
		return
	}

	resp := sandboxToResponse(sb)

	diskBytes, err := h.svc.GetDiskUsage(r.Context(), sandboxID, ac.TeamID)
	if err == nil {
		diskUsedMB := diskBytes / (1024 * 1024)
		resp.DiskUsedMB = &diskUsedMB
	}

	writeJSON(w, http.StatusOK, resp)
}

// Pause handles POST /v1/capsules/{id}/pause.
func (h *sandboxHandler) Pause(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid sandbox ID."))
		return
	}

	sb, err := h.svc.Pause(r.Context(), sandboxID, ac.TeamID)
	h.audit.LogSandboxPause(r.Context(), ac, sandboxID, err)
	if err != nil {
		writeErr(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, sandboxToResponse(sb))
}

// Resume handles POST /v1/capsules/{id}/resume.
func (h *sandboxHandler) Resume(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid sandbox ID."))
		return
	}

	sb, err := h.svc.Resume(r.Context(), sandboxID, ac.TeamID)
	h.audit.LogSandboxResume(r.Context(), ac, sandboxID, err)
	if err != nil {
		writeErr(w, r, err)
		return
	}

	writeJSON(w, http.StatusAccepted, sandboxToResponse(sb))
}

// Ping handles POST /v1/capsules/{id}/ping.
func (h *sandboxHandler) Ping(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid sandbox ID."))
		return
	}

	if err := h.svc.Ping(r.Context(), sandboxID, ac.TeamID); err != nil {
		writeErr(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Destroy handles DELETE /v1/capsules/{id}.
func (h *sandboxHandler) Destroy(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid sandbox ID."))
		return
	}

	err = h.svc.Destroy(r.Context(), sandboxID, ac.TeamID)
	h.audit.LogSandboxDestroy(r.Context(), ac, sandboxID, err)
	if err != nil {
		writeErr(w, r, err)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}
