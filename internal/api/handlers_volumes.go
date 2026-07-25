package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/wrenn/internal/units"
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
	// Name is optional; omitted, the volume is named after its own ID.
	Name string `json:"name"`
	// Size is the human-readable form ("20Gi", "500M"). SizeMB is the plain
	// megabyte form. Exactly one is needed; Size wins if both are given.
	Size   string `json:"size"`
	SizeMB int32  `json:"size_mb"`
}

// sizeMB resolves the request's size into megabytes, accepting either field.
func (r createVolumeRequest) sizeMB() (int32, error) {
	if s := strings.TrimSpace(r.Size); s != "" {
		mb, err := units.ParseSizeToMB(s)
		if err != nil {
			return 0, apperr.InvalidRequest.WrapMsg(err, "Invalid volume size.")
		}
		return int32(mb), nil
	}
	if r.SizeMB <= 0 {
		return 0, apperr.InvalidRequest.Msg(`A volume size is required (e.g. "size": "20Gi" or "size_mb": 20480).`)
	}
	return r.SizeMB, nil
}

type volumeResponse struct {
	ID        string  `json:"id"`
	TeamID    string  `json:"team_id"`
	Name      string  `json:"name"`
	SizeMB    int32   `json:"size_mb"`
	Status    string  `json:"status"`
	HostID    *string `json:"host_id,omitempty"`
	SandboxID *string `json:"sandbox_id,omitempty"`
	// Always emitted, "" while detached — host_id/sandbox_id above are pointers
	// because they are genuinely nullable, but mount_path is not.
	MountPath      string  `json:"mount_path"`
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

	sizeMB, err := req.sizeMB()
	if err != nil {
		writeErr(w, r, err)
		return
	}

	vol, err := h.svc.Create(r.Context(), ac.TeamID, req.Name, sizeMB)
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

// Get handles GET /v1/volumes/{id}, where {id} is a volume ID or name.
func (h *volumeHandler) Get(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	vol, err := h.svc.Resolve(r.Context(), ac.TeamID, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, volumeToResponse(vol))
}

// Delete handles DELETE /v1/volumes/{id}, where {id} is a volume ID or name.
func (h *volumeHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	vol, err := h.svc.Resolve(r.Context(), ac.TeamID, chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, r, err)
		return
	}

	if err := h.svc.Delete(r.Context(), vol.ID, ac.TeamID); err != nil {
		writeErr(w, r, err)
		return
	}

	h.audit.LogVolumeDelete(r.Context(), ac, vol.ID, nil)
	w.WriteHeader(http.StatusNoContent)
}
