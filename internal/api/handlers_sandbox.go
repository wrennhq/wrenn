package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/wrenn/internal/audit"
	"git.omukk.dev/wrenn/wrenn/internal/auth"
	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/id"
	"git.omukk.dev/wrenn/wrenn/internal/service"
)

type sandboxHandler struct {
	svc   *service.SandboxService
	audit *audit.AuditLogger
}

func newSandboxHandler(svc *service.SandboxService, al *audit.AuditLogger) *sandboxHandler {
	return &sandboxHandler{svc: svc, audit: al}
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
		ID:         id.FormatSandboxID(sb.ID),
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

// Create handles POST /v1/capsules.
func (h *sandboxHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	ac := auth.MustFromContext(r.Context())
	if !ac.TeamID.Valid {
		writeError(w, http.StatusForbidden, "no_team", "no active team context; re-authenticate")
		return
	}

	sb, err := h.svc.Create(r.Context(), service.SandboxCreateParams{
		TeamID:     ac.TeamID,
		Template:   req.Template,
		VCPUs:      req.VCPUs,
		MemoryMB:   req.MemoryMB,
		TimeoutSec: req.TimeoutSec,
	})
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	h.audit.LogSandboxCreate(r.Context(), ac, sb.ID, sb.Template)
	writeJSON(w, http.StatusCreated, sandboxToResponse(sb))
}

// List handles GET /v1/capsules.
func (h *sandboxHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	sandboxes, err := h.svc.List(r.Context(), ac.TeamID)
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

// Get handles GET /v1/capsules/{id}.
func (h *sandboxHandler) Get(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	sb, err := h.svc.Get(r.Context(), sandboxID, ac.TeamID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	writeJSON(w, http.StatusOK, sandboxToResponse(sb))
}

// Pause handles POST /v1/capsules/{id}/pause.
func (h *sandboxHandler) Pause(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	sb, err := h.svc.Pause(r.Context(), sandboxID, ac.TeamID)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	h.audit.LogSandboxPause(r.Context(), ac, sandboxID)
	writeJSON(w, http.StatusOK, sandboxToResponse(sb))
}

// Resume handles POST /v1/capsules/{id}/resume.
func (h *sandboxHandler) Resume(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	sb, err := h.svc.Resume(r.Context(), sandboxID, ac.TeamID)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	h.audit.LogSandboxResume(r.Context(), ac, sandboxID)
	writeJSON(w, http.StatusOK, sandboxToResponse(sb))
}

// Ping handles POST /v1/capsules/{id}/ping.
func (h *sandboxHandler) Ping(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	if err := h.svc.Ping(r.Context(), sandboxID, ac.TeamID); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
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
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	if err := h.svc.Destroy(r.Context(), sandboxID, ac.TeamID); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	h.audit.LogSandboxDestroy(r.Context(), ac, sandboxID)
	w.WriteHeader(http.StatusNoContent)
}
