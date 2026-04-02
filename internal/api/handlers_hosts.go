package api

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/audit"
	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/service"
)

type hostHandler struct {
	svc     *service.HostService
	queries *db.Queries
	audit   *audit.AuditLogger
}

func newHostHandler(svc *service.HostService, queries *db.Queries, al *audit.AuditLogger) *hostHandler {
	return &hostHandler{svc: svc, queries: queries, audit: al}
}

// Request/response types.

type createHostRequest struct {
	Type             string `json:"type"`
	TeamID           string `json:"team_id,omitempty"`
	Provider         string `json:"provider,omitempty"`
	AvailabilityZone string `json:"availability_zone,omitempty"`
}

type createHostResponse struct {
	Host              hostResponse `json:"host"`
	RegistrationToken string       `json:"registration_token"`
}

type refreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type refreshTokenResponse struct {
	Host         hostResponse `json:"host"`
	Token        string       `json:"token"`
	RefreshToken string       `json:"refresh_token"`
	CertPEM      string       `json:"cert_pem,omitempty"`
	KeyPEM       string       `json:"key_pem,omitempty"`
	CACertPEM    string       `json:"ca_cert_pem,omitempty"`
}

type deletePreviewResponse struct {
	Host       hostResponse `json:"host"`
	SandboxIDs []string     `json:"sandbox_ids"`
}

type registerHostRequest struct {
	Token    string `json:"token"`
	Arch     string `json:"arch,omitempty"`
	CPUCores int32  `json:"cpu_cores,omitempty"`
	MemoryMB int32  `json:"memory_mb,omitempty"`
	DiskGB   int32  `json:"disk_gb,omitempty"`
	Address  string `json:"address"`
}

type registerHostResponse struct {
	Host         hostResponse `json:"host"`
	Token        string       `json:"token"`
	RefreshToken string       `json:"refresh_token"`
	CertPEM      string       `json:"cert_pem,omitempty"`
	KeyPEM       string       `json:"key_pem,omitempty"`
	CACertPEM    string       `json:"ca_cert_pem,omitempty"`
}

type addTagRequest struct {
	Tag string `json:"tag"`
}

type hostResponse struct {
	ID               string  `json:"id"`
	Type             string  `json:"type"`
	TeamID           *string `json:"team_id,omitempty"`
	TeamName         *string `json:"team_name,omitempty"`
	Provider         *string `json:"provider,omitempty"`
	AvailabilityZone *string `json:"availability_zone,omitempty"`
	Arch             *string `json:"arch,omitempty"`
	CPUCores         *int32  `json:"cpu_cores,omitempty"`
	MemoryMB         *int32  `json:"memory_mb,omitempty"`
	DiskGB           *int32  `json:"disk_gb,omitempty"`
	Address          *string `json:"address,omitempty"`
	Status           string  `json:"status"`
	LastHeartbeatAt  *string `json:"last_heartbeat_at,omitempty"`
	CreatedBy        string  `json:"created_by"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

func hostToResponse(h db.Host) hostResponse {
	resp := hostResponse{
		ID:        id.FormatHostID(h.ID),
		Type:      h.Type,
		Status:    h.Status,
		CreatedBy: id.FormatUserID(h.CreatedBy),
	}
	if h.TeamID.Valid {
		s := id.FormatTeamID(h.TeamID)
		resp.TeamID = &s
	}
	if h.Provider != "" {
		resp.Provider = &h.Provider
	}
	if h.AvailabilityZone != "" {
		resp.AvailabilityZone = &h.AvailabilityZone
	}
	if h.Arch != "" {
		resp.Arch = &h.Arch
	}
	if h.CpuCores != 0 {
		resp.CPUCores = &h.CpuCores
	}
	if h.MemoryMb != 0 {
		resp.MemoryMB = &h.MemoryMb
	}
	if h.DiskGb != 0 {
		resp.DiskGB = &h.DiskGb
	}
	if h.Address != "" {
		resp.Address = &h.Address
	}
	if h.LastHeartbeatAt.Valid {
		s := h.LastHeartbeatAt.Time.Format(time.RFC3339)
		resp.LastHeartbeatAt = &s
	}
	// created_at and updated_at are NOT NULL DEFAULT NOW(), always valid.
	resp.CreatedAt = h.CreatedAt.Time.Format(time.RFC3339)
	resp.UpdatedAt = h.UpdatedAt.Time.Format(time.RFC3339)
	return resp
}

// isAdmin fetches the user record and returns whether they are an admin.
func (h *hostHandler) isAdmin(r *http.Request, userID pgtype.UUID) bool {
	user, err := h.queries.GetUserByID(r.Context(), userID)
	if err != nil {
		return false
	}
	return user.IsAdmin
}

// Create handles POST /v1/hosts.
func (h *hostHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createHostRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	ac := auth.MustFromContext(r.Context())

	// Parse optional team ID from request body.
	var params service.HostCreateParams
	params.Type = req.Type
	params.Provider = req.Provider
	params.AvailabilityZone = req.AvailabilityZone
	params.RequestingUserID = ac.UserID
	params.IsRequestorAdmin = h.isAdmin(r, ac.UserID)
	if req.TeamID != "" {
		teamID, err := id.ParseTeamID(req.TeamID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid team_id")
			return
		}
		params.TeamID = teamID
	}

	result, err := h.svc.Create(r.Context(), params)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	// Log audit for the owning team (BYOC hosts have a team; shared hosts use caller's team).
	h.audit.LogHostCreate(r.Context(), ac, result.Host.ID, result.Host.TeamID)

	writeJSON(w, http.StatusCreated, createHostResponse{
		Host:              hostToResponse(result.Host),
		RegistrationToken: result.RegistrationToken,
	})
}

// List handles GET /v1/hosts.
func (h *hostHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	admin := h.isAdmin(r, ac.UserID)

	hosts, err := h.svc.List(r.Context(), ac.TeamID, admin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list hosts")
		return
	}

	// Collect unique team IDs so we can fetch team names in one pass.
	var teamNames map[string]string
	if admin {
		seen := make(map[string]struct{})
		for _, host := range hosts {
			if host.TeamID.Valid {
				key := id.FormatTeamID(host.TeamID)
				seen[key] = struct{}{}
			}
		}
		if len(seen) > 0 {
			teamNames = make(map[string]string, len(seen))
			for _, host := range hosts {
				if !host.TeamID.Valid {
					continue
				}
				key := id.FormatTeamID(host.TeamID)
				if _, ok := teamNames[key]; ok {
					continue
				}
				if team, err := h.queries.GetTeam(r.Context(), host.TeamID); err == nil {
					teamNames[key] = team.Name
				}
			}
		}
	}

	resp := make([]hostResponse, len(hosts))
	for i, host := range hosts {
		resp[i] = hostToResponse(host)
		if host.TeamID.Valid {
			key := id.FormatTeamID(host.TeamID)
			if name, ok := teamNames[key]; ok {
				resp[i].TeamName = &name
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// Get handles GET /v1/hosts/{id}.
func (h *hostHandler) Get(w http.ResponseWriter, r *http.Request) {
	hostIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	hostID, err := id.ParseHostID(hostIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid host ID")
		return
	}

	host, err := h.svc.Get(r.Context(), hostID, ac.TeamID, h.isAdmin(r, ac.UserID))
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, hostToResponse(host))
}

// DeletePreview handles GET /v1/hosts/{id}/delete-preview.
// Returns what would be affected without making changes, for confirmation UI.
func (h *hostHandler) DeletePreview(w http.ResponseWriter, r *http.Request) {
	hostIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	hostID, err := id.ParseHostID(hostIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid host ID")
		return
	}

	preview, err := h.svc.DeletePreview(r.Context(), hostID, ac.TeamID, h.isAdmin(r, ac.UserID))
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, deletePreviewResponse{
		Host:       hostToResponse(preview.Host),
		SandboxIDs: preview.SandboxIDs,
	})
}

// Delete handles DELETE /v1/hosts/{id}.
// Without ?force=true: returns 409 with affected sandbox IDs if any are active.
// With ?force=true: gracefully stops all sandboxes then deletes the host.
func (h *hostHandler) Delete(w http.ResponseWriter, r *http.Request) {
	hostIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())
	force := r.URL.Query().Get("force") == "true"

	hostID, err := id.ParseHostID(hostIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid host ID")
		return
	}

	// Fetch host before deletion to capture team_id for audit.
	deletedHost, hostErr := h.queries.GetHost(r.Context(), hostID)
	if hostErr != nil {
		slog.Warn("audit: could not fetch host before delete", "host_id", hostIDStr, "error", hostErr)
	}

	err = h.svc.Delete(r.Context(), hostID, ac.UserID, ac.TeamID, h.isAdmin(r, ac.UserID), force)
	if err == nil {
		h.audit.LogHostDelete(r.Context(), ac, hostID, deletedHost.TeamID)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Check if it's a "has running sandboxes" error and return a structured 409.
	var hasSandboxes *service.HostHasSandboxesError
	if errors.As(err, &hasSandboxes) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]any{
				"code":        "has_active_sandboxes",
				"message":     "host has active sandboxes; use ?force=true to destroy them and delete the host",
				"sandbox_ids": hasSandboxes.SandboxIDs,
			},
		})
		return
	}

	status, code, msg := serviceErrToHTTP(err)
	writeError(w, status, code, msg)
}

// RegenerateToken handles POST /v1/hosts/{id}/token.
func (h *hostHandler) RegenerateToken(w http.ResponseWriter, r *http.Request) {
	hostIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	hostID, err := id.ParseHostID(hostIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid host ID")
		return
	}

	result, err := h.svc.RegenerateToken(r.Context(), hostID, ac.UserID, ac.TeamID, h.isAdmin(r, ac.UserID))
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusCreated, createHostResponse{
		Host:              hostToResponse(result.Host),
		RegistrationToken: result.RegistrationToken,
	})
}

// Register handles POST /v1/hosts/register (unauthenticated).
func (h *hostHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerHostRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	if req.Address == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "address is required")
		return
	}

	result, err := h.svc.Register(r.Context(), service.HostRegisterParams{
		Token:    req.Token,
		Arch:     req.Arch,
		CPUCores: req.CPUCores,
		MemoryMB: req.MemoryMB,
		DiskGB:   req.DiskGB,
		Address:  req.Address,
	})
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusCreated, registerHostResponse{
		Host:         hostToResponse(result.Host),
		Token:        result.JWT,
		RefreshToken: result.RefreshToken,
		CertPEM:      result.CertPEM,
		KeyPEM:       result.KeyPEM,
		CACertPEM:    result.CACertPEM,
	})
}

// Heartbeat handles POST /v1/hosts/{id}/heartbeat (host-token-authenticated).
func (h *hostHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	hostIDStr := chi.URLParam(r, "id")
	hc := auth.MustHostFromContext(r.Context())

	hostID, err := id.ParseHostID(hostIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid host ID")
		return
	}

	// Prevent a host from heartbeating for a different host.
	if hostID != hc.HostID {
		writeError(w, http.StatusForbidden, "forbidden", "host ID mismatch")
		return
	}

	// Capture pre-heartbeat status to detect unreachable → online transition.
	prevHost, _ := h.queries.GetHost(r.Context(), hc.HostID)

	if err := h.svc.Heartbeat(r.Context(), hc.HostID); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	// Log marked_up if the host just recovered from unreachable.
	if prevHost.Status == "unreachable" {
		h.audit.LogHostMarkedUp(r.Context(), prevHost.TeamID, hc.HostID)
	}

	w.WriteHeader(http.StatusNoContent)
}

// AddTag handles POST /v1/hosts/{id}/tags.
func (h *hostHandler) AddTag(w http.ResponseWriter, r *http.Request) {
	hostIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())
	admin := h.isAdmin(r, ac.UserID)

	hostID, err := id.ParseHostID(hostIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid host ID")
		return
	}

	var req addTagRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Tag == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "tag is required")
		return
	}

	if err := h.svc.AddTag(r.Context(), hostID, ac.TeamID, admin, req.Tag); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RemoveTag handles DELETE /v1/hosts/{id}/tags/{tag}.
func (h *hostHandler) RemoveTag(w http.ResponseWriter, r *http.Request) {
	hostIDStr := chi.URLParam(r, "id")
	tag := chi.URLParam(r, "tag")
	ac := auth.MustFromContext(r.Context())

	hostID, err := id.ParseHostID(hostIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid host ID")
		return
	}

	if err := h.svc.RemoveTag(r.Context(), hostID, ac.TeamID, h.isAdmin(r, ac.UserID), tag); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RefreshToken handles POST /v1/hosts/auth/refresh (unauthenticated).
// The host agent sends its refresh token to receive a new JWT and rotated refresh token.
func (h *hostHandler) RefreshToken(w http.ResponseWriter, r *http.Request) {
	var req refreshTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}

	result, err := h.svc.Refresh(r.Context(), req.RefreshToken)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, refreshTokenResponse{
		Host:         hostToResponse(result.Host),
		Token:        result.JWT,
		RefreshToken: result.RefreshToken,
		CertPEM:      result.CertPEM,
		KeyPEM:       result.KeyPEM,
		CACertPEM:    result.CACertPEM,
	})
}

// ListTags handles GET /v1/hosts/{id}/tags.
func (h *hostHandler) ListTags(w http.ResponseWriter, r *http.Request) {
	hostIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	hostID, err := id.ParseHostID(hostIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid host ID")
		return
	}

	tags, err := h.svc.ListTags(r.Context(), hostID, ac.TeamID, h.isAdmin(r, ac.UserID))
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, tags)
}
