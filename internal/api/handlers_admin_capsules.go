package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
)

type adminCapsuleHandler struct {
	svc   *service.SandboxService
	db    *db.Queries
	pool  *lifecycle.HostClientPool
	audit *audit.AuditLogger
}

func newAdminCapsuleHandler(svc *service.SandboxService, db *db.Queries, pool *lifecycle.HostClientPool, al *audit.AuditLogger) *adminCapsuleHandler {
	return &adminCapsuleHandler{svc: svc, db: db, pool: pool, audit: al}
}

// Create handles POST /v1/admin/capsules.
func (h *adminCapsuleHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	ac := auth.MustFromContext(r.Context())

	sb, err := h.svc.Create(r.Context(), service.SandboxCreateParams{
		TeamID:     id.PlatformTeamID,
		Template:   req.Template,
		VCPUs:      req.VCPUs,
		MemoryMB:   req.MemoryMB,
		DiskSizeMB: req.DiskSizeMB,
		TimeoutSec: req.TimeoutSec,
	})
	ac.TeamID = id.PlatformTeamID
	h.audit.LogSandboxCreate(r.Context(), ac, sb.ID, req.Template, err)
	if err != nil {
		if sb.ID.Valid {
			h.audit.LogSandboxDestroySystem(r.Context(), id.PlatformTeamID, sb.ID, "cleanup_after_create_error", nil)
		}
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusAccepted, sandboxToResponse(sb))
}

// List handles GET /v1/admin/capsules.
func (h *adminCapsuleHandler) List(w http.ResponseWriter, r *http.Request) {
	sandboxes, err := h.svc.List(r.Context(), id.PlatformTeamID)
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

// Get handles GET /v1/admin/capsules/{id}.
func (h *adminCapsuleHandler) Get(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	sb, err := h.svc.Get(r.Context(), sandboxID, id.PlatformTeamID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	writeJSON(w, http.StatusOK, sandboxToResponse(sb))
}

// Destroy handles DELETE /v1/admin/capsules/{id}.
func (h *adminCapsuleHandler) Destroy(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	ac.TeamID = id.PlatformTeamID
	err = h.svc.Destroy(r.Context(), sandboxID, id.PlatformTeamID)
	h.audit.LogSandboxDestroy(r.Context(), ac, sandboxID, err)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

type adminSnapshotRequest struct {
	Name string `json:"name"`
}

// Snapshot handles POST /v1/admin/capsules/{id}/snapshot. Takes a live
// snapshot of a platform-owned capsule and registers the result as a
// platform template (team_id = 00000000-...).
func (h *adminCapsuleHandler) Snapshot(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	var req adminSnapshotRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
			return
		}
	}

	sb, name, err := h.svc.CreateSnapshot(r.Context(), sandboxID, id.PlatformTeamID, req.Name)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}
	ac := auth.MustFromContext(r.Context())
	ac.TeamID = id.PlatformTeamID
	h.audit.LogSnapshotCreateRequested(r.Context(), ac, name)

	writeJSON(w, http.StatusAccepted, sandboxToResponse(sb))
}
