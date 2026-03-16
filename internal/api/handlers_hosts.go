package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/service"
)

type hostHandler struct {
	svc     *service.HostService
	queries *db.Queries
}

func newHostHandler(svc *service.HostService, queries *db.Queries) *hostHandler {
	return &hostHandler{svc: svc, queries: queries}
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

type registerHostRequest struct {
	Token    string `json:"token"`
	Arch     string `json:"arch,omitempty"`
	CPUCores int32  `json:"cpu_cores,omitempty"`
	MemoryMB int32  `json:"memory_mb,omitempty"`
	DiskGB   int32  `json:"disk_gb,omitempty"`
	Address  string `json:"address"`
}

type registerHostResponse struct {
	Host  hostResponse `json:"host"`
	Token string       `json:"token"`
}

type addTagRequest struct {
	Tag string `json:"tag"`
}

type hostResponse struct {
	ID               string  `json:"id"`
	Type             string  `json:"type"`
	TeamID           *string `json:"team_id,omitempty"`
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
		ID:        h.ID,
		Type:      h.Type,
		Status:    h.Status,
		CreatedBy: h.CreatedBy,
	}
	if h.TeamID.Valid {
		resp.TeamID = &h.TeamID.String
	}
	if h.Provider.Valid {
		resp.Provider = &h.Provider.String
	}
	if h.AvailabilityZone.Valid {
		resp.AvailabilityZone = &h.AvailabilityZone.String
	}
	if h.Arch.Valid {
		resp.Arch = &h.Arch.String
	}
	if h.CpuCores.Valid {
		resp.CPUCores = &h.CpuCores.Int32
	}
	if h.MemoryMb.Valid {
		resp.MemoryMB = &h.MemoryMb.Int32
	}
	if h.DiskGb.Valid {
		resp.DiskGB = &h.DiskGb.Int32
	}
	if h.Address.Valid {
		resp.Address = &h.Address.String
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
func (h *hostHandler) isAdmin(r *http.Request, userID string) bool {
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

	result, err := h.svc.Create(r.Context(), service.HostCreateParams{
		Type:             req.Type,
		TeamID:           req.TeamID,
		Provider:         req.Provider,
		AvailabilityZone: req.AvailabilityZone,
		RequestingUserID: ac.UserID,
		IsRequestorAdmin: h.isAdmin(r, ac.UserID),
	})
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

// List handles GET /v1/hosts.
func (h *hostHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	hosts, err := h.svc.List(r.Context(), ac.TeamID, h.isAdmin(r, ac.UserID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list hosts")
		return
	}

	resp := make([]hostResponse, len(hosts))
	for i, host := range hosts {
		resp[i] = hostToResponse(host)
	}

	writeJSON(w, http.StatusOK, resp)
}

// Get handles GET /v1/hosts/{id}.
func (h *hostHandler) Get(w http.ResponseWriter, r *http.Request) {
	hostID := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	host, err := h.svc.Get(r.Context(), hostID, ac.TeamID, h.isAdmin(r, ac.UserID))
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, hostToResponse(host))
}

// Delete handles DELETE /v1/hosts/{id}.
func (h *hostHandler) Delete(w http.ResponseWriter, r *http.Request) {
	hostID := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	if err := h.svc.Delete(r.Context(), hostID, ac.UserID, ac.TeamID, h.isAdmin(r, ac.UserID)); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RegenerateToken handles POST /v1/hosts/{id}/token.
func (h *hostHandler) RegenerateToken(w http.ResponseWriter, r *http.Request) {
	hostID := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

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
		Host:  hostToResponse(result.Host),
		Token: result.JWT,
	})
}

// Heartbeat handles POST /v1/hosts/{id}/heartbeat (host-token-authenticated).
func (h *hostHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	hostID := chi.URLParam(r, "id")
	hc := auth.MustHostFromContext(r.Context())

	// Prevent a host from heartbeating for a different host.
	if hostID != hc.HostID {
		writeError(w, http.StatusForbidden, "forbidden", "host ID mismatch")
		return
	}

	if err := h.svc.Heartbeat(r.Context(), hc.HostID); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to update heartbeat")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// AddTag handles POST /v1/hosts/{id}/tags.
func (h *hostHandler) AddTag(w http.ResponseWriter, r *http.Request) {
	hostID := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())
	admin := h.isAdmin(r, ac.UserID)

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
	hostID := chi.URLParam(r, "id")
	tag := chi.URLParam(r, "tag")
	ac := auth.MustFromContext(r.Context())

	if err := h.svc.RemoveTag(r.Context(), hostID, ac.TeamID, h.isAdmin(r, ac.UserID), tag); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ListTags handles GET /v1/hosts/{id}/tags.
func (h *hostHandler) ListTags(w http.ResponseWriter, r *http.Request) {
	hostID := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	tags, err := h.svc.ListTags(r.Context(), hostID, ac.TeamID, h.isAdmin(r, ac.UserID))
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, tags)
}
