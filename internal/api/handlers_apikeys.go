package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
)

type apiKeyHandler struct {
	svc   *service.APIKeyService
	audit *audit.AuditLogger
}

func newAPIKeyHandler(svc *service.APIKeyService, al *audit.AuditLogger) *apiKeyHandler {
	return &apiKeyHandler{svc: svc, audit: al}
}

type createAPIKeyRequest struct {
	Name string `json:"name"`
}

type apiKeyResponse struct {
	ID           string  `json:"id"`
	TeamID       string  `json:"team_id"`
	Name         string  `json:"name"`
	KeyPrefix    string  `json:"key_prefix"`
	CreatedBy    string  `json:"created_by"`
	CreatorEmail string  `json:"creator_email,omitempty"`
	CreatedAt    string  `json:"created_at"`
	LastUsed     *string `json:"last_used,omitempty"`
	Key          *string `json:"key,omitempty"` // only populated on Create
}

func apiKeyToResponse(k db.TeamApiKey) apiKeyResponse {
	resp := apiKeyResponse{
		ID:        id.FormatAPIKeyID(k.ID),
		TeamID:    id.FormatTeamID(k.TeamID),
		Name:      k.Name,
		KeyPrefix: k.KeyPrefix,
		CreatedBy: id.FormatUserID(k.CreatedBy),
	}
	if k.CreatedAt.Valid {
		resp.CreatedAt = k.CreatedAt.Time.Format(time.RFC3339)
	}
	if k.LastUsed.Valid {
		s := k.LastUsed.Time.Format(time.RFC3339)
		resp.LastUsed = &s
	}
	return resp
}

func apiKeyWithCreatorToResponse(k db.ListAPIKeysByTeamWithCreatorRow) apiKeyResponse {
	resp := apiKeyResponse{
		ID:           id.FormatAPIKeyID(k.ID),
		TeamID:       id.FormatTeamID(k.TeamID),
		Name:         k.Name,
		KeyPrefix:    k.KeyPrefix,
		CreatedBy:    id.FormatUserID(k.CreatedBy),
		CreatorEmail: k.CreatorEmail,
	}
	if k.CreatedAt.Valid {
		resp.CreatedAt = k.CreatedAt.Time.Format(time.RFC3339)
	}
	if k.LastUsed.Valid {
		s := k.LastUsed.Time.Format(time.RFC3339)
		resp.LastUsed = &s
	}
	return resp
}

// Create handles POST /v1/api-keys.
func (h *apiKeyHandler) Create(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	var req createAPIKeyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	result, err := h.svc.Create(r.Context(), ac.TeamID, ac.UserID, req.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to create API key")
		return
	}

	resp := apiKeyToResponse(result.Row)
	resp.Key = &result.Plaintext

	h.audit.LogAPIKeyCreate(r.Context(), ac, result.Row.ID, result.Row.Name)
	writeJSON(w, http.StatusCreated, resp)
}

// List handles GET /v1/api-keys.
func (h *apiKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	keys, err := h.svc.ListWithCreator(r.Context(), ac.TeamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list API keys")
		return
	}

	resp := make([]apiKeyResponse, len(keys))
	for i, k := range keys {
		resp[i] = apiKeyWithCreatorToResponse(k)
	}

	writeJSON(w, http.StatusOK, resp)
}

// Delete handles DELETE /v1/api-keys/{id}.
func (h *apiKeyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	keyIDStr := chi.URLParam(r, "id")

	keyID, err := id.ParseAPIKeyID(keyIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid API key ID")
		return
	}

	if err := h.svc.Delete(r.Context(), keyID, ac.TeamID); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to delete API key")
		return
	}

	h.audit.LogAPIKeyRevoke(r.Context(), ac, keyID)
	w.WriteHeader(http.StatusNoContent)
}
