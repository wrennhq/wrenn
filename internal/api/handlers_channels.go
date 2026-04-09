package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"git.omukk.dev/wrenn/sandbox/internal/audit"
	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/channels"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
)

type channelHandler struct {
	svc   *channels.Service
	audit *audit.AuditLogger
}

func newChannelHandler(svc *channels.Service, al *audit.AuditLogger) *channelHandler {
	return &channelHandler{svc: svc, audit: al}
}

type createChannelRequest struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Config   map[string]string `json:"config"`
	Events   []string          `json:"events"`
}

type updateChannelRequest struct {
	Name   string   `json:"name"`
	Events []string `json:"events"`
}

type rotateConfigRequest struct {
	Config map[string]string `json:"config"`
}

type testChannelRequest struct {
	Provider string            `json:"provider"`
	Config   map[string]string `json:"config"`
}

type channelResponse struct {
	ID        string   `json:"id"`
	TeamID    string   `json:"team_id"`
	Name      string   `json:"name"`
	Provider  string   `json:"provider"`
	Events    []string `json:"events"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	Secret    *string  `json:"secret,omitempty"`
}

func channelToResponse(ch db.Channel) channelResponse {
	resp := channelResponse{
		ID:       id.FormatChannelID(ch.ID),
		TeamID:   id.FormatTeamID(ch.TeamID),
		Name:     ch.Name,
		Provider: ch.Provider,
		Events:   ch.EventTypes,
	}
	if ch.CreatedAt.Valid {
		resp.CreatedAt = ch.CreatedAt.Time.Format(time.RFC3339)
	}
	if ch.UpdatedAt.Valid {
		resp.UpdatedAt = ch.UpdatedAt.Time.Format(time.RFC3339)
	}
	return resp
}

// Create handles POST /v1/channels.
func (h *channelHandler) Create(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	var req createChannelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	result, err := h.svc.Create(r.Context(), channels.CreateParams{
		TeamID:   ac.TeamID,
		Name:     req.Name,
		Provider: req.Provider,
		Config:   req.Config,
		Events:   req.Events,
	})
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	h.audit.LogChannelCreate(r.Context(), ac, result.Channel.ID, result.Channel.Name, result.Channel.Provider)

	resp := channelToResponse(result.Channel)
	if result.PlaintextSecret != "" {
		resp.Secret = &result.PlaintextSecret
	}

	writeJSON(w, http.StatusCreated, resp)
}

// List handles GET /v1/channels.
func (h *channelHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	chs, err := h.svc.List(r.Context(), ac.TeamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list channels")
		return
	}

	resp := make([]channelResponse, len(chs))
	for i, ch := range chs {
		resp[i] = channelToResponse(ch)
	}

	writeJSON(w, http.StatusOK, resp)
}

// Get handles GET /v1/channels/{id}.
func (h *channelHandler) Get(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	channelIDStr := chi.URLParam(r, "id")

	channelID, err := id.ParseChannelID(channelIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid channel ID")
		return
	}

	ch, err := h.svc.Get(r.Context(), channelID, ac.TeamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "channel not found")
		} else {
			writeError(w, http.StatusInternalServerError, "db_error", "failed to get channel")
		}
		return
	}

	writeJSON(w, http.StatusOK, channelToResponse(ch))
}

// Update handles PATCH /v1/channels/{id}.
func (h *channelHandler) Update(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	channelIDStr := chi.URLParam(r, "id")

	channelID, err := id.ParseChannelID(channelIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid channel ID")
		return
	}

	var req updateChannelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	ch, err := h.svc.Update(r.Context(), channelID, ac.TeamID, req.Name, req.Events)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	h.audit.LogChannelUpdate(r.Context(), ac, channelID)
	writeJSON(w, http.StatusOK, channelToResponse(ch))
}

// Test handles POST /v1/channels/test.
func (h *channelHandler) Test(w http.ResponseWriter, r *http.Request) {
	var req testChannelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if err := h.svc.Test(r.Context(), req.Provider, req.Config); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// RotateConfig handles PUT /v1/channels/{id}/config.
func (h *channelHandler) RotateConfig(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	channelIDStr := chi.URLParam(r, "id")

	channelID, err := id.ParseChannelID(channelIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid channel ID")
		return
	}

	var req rotateConfigRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	ch, err := h.svc.RotateConfig(r.Context(), channelID, ac.TeamID, req.Config)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	h.audit.LogChannelRotateConfig(r.Context(), ac, channelID)
	writeJSON(w, http.StatusOK, channelToResponse(ch))
}

// Delete handles DELETE /v1/channels/{id}.
func (h *channelHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	channelIDStr := chi.URLParam(r, "id")

	channelID, err := id.ParseChannelID(channelIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid channel ID")
		return
	}

	if err := h.svc.Delete(r.Context(), channelID, ac.TeamID); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to delete channel")
		return
	}

	h.audit.LogChannelDelete(r.Context(), ac, channelID)
	w.WriteHeader(http.StatusNoContent)
}
