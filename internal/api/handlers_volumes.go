package api

import (
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

type volumeHandler struct {
	svc   *service.VolumeService
	audit *audit.AuditLogger
}

func newVolumeHandler(svc *service.VolumeService, al *audit.AuditLogger) *volumeHandler {
	return &volumeHandler{svc: svc, audit: al}
}

type createVolumeRequest struct {
	Name   string `json:"name"`
	SizeMB int32  `json:"size_mb"`
}

type volumeResponse struct {
	ID             string  `json:"id"`
	TeamID         string  `json:"team_id"`
	Name           string  `json:"name"`
	SizeMB         int32   `json:"size_mb"`
	Status         string  `json:"status"`
	HostID         *string `json:"host_id,omitempty"`
	SandboxID      *string `json:"sandbox_id,omitempty"`
	MountPath      string  `json:"mount_path,omitempty"`
	CreatedAt      string  `json:"created_at"`
	LastAttachedAt *string `json:"last_attached_at,omitempty"`
}

func volumeToResponse(v db.Volume) volumeResponse {
	resp := volumeResponse{
		ID:        id.FormatVolumeID(v.ID),
		TeamID:    id.FormatTeamID(v.TeamID),
		Name:      v.Name,
		SizeMB:    v.SizeMb,
		Status:    v.Status,
		MountPath: v.MountPath,
	}
	if v.HostID.Valid {
		s := id.FormatHostID(v.HostID)
		resp.HostID = &s
	}
	if v.SandboxID.Valid {
		s := id.FormatSandboxID(v.SandboxID)
		resp.SandboxID = &s
	}
	if v.CreatedAt.Valid {
		resp.CreatedAt = v.CreatedAt.Time.Format(time.RFC3339)
	}
	if v.LastAttachedAt.Valid {
		s := v.LastAttachedAt.Time.Format(time.RFC3339)
		resp.LastAttachedAt = &s
	}
	return resp
}

// Create handles POST /v1/volumes.
func (h *volumeHandler) Create(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	var req createVolumeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}

	vol, err := h.svc.Create(r.Context(), ac.TeamID, req.Name, req.SizeMB)
	if err != nil {
		writeErr(w, r, err)
		return
	}

	h.audit.LogVolumeCreate(r.Context(), ac, vol.ID, vol.Name, int(vol.SizeMb), nil)
	writeJSON(w, http.StatusCreated, volumeToResponse(vol))
}

// List handles GET /v1/volumes.
func (h *volumeHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	vols, err := h.svc.List(r.Context(), ac.TeamID)
	if err != nil {
		writeErr(w, r, err)
		return
	}

	resp := make([]volumeResponse, len(vols))
	for i, v := range vols {
		resp[i] = volumeToResponse(v)
	}
	writeJSON(w, http.StatusOK, resp)
}

// Get handles GET /v1/volumes/{id}.
func (h *volumeHandler) Get(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	volumeID, err := id.ParseVolumeID(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid volume ID."))
		return
	}

	vol, err := h.svc.Get(r.Context(), volumeID, ac.TeamID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, volumeToResponse(vol))
}

// Delete handles DELETE /v1/volumes/{id}.
func (h *volumeHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	volumeID, err := id.ParseVolumeID(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid volume ID."))
		return
	}

	if err := h.svc.Delete(r.Context(), volumeID, ac.TeamID); err != nil {
		writeErr(w, r, err)
		return
	}

	h.audit.LogVolumeDelete(r.Context(), ac, volumeID, nil)
	w.WriteHeader(http.StatusNoContent)
}
