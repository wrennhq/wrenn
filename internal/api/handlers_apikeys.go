package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
)

type apiKeyHandler struct {
	db *db.Queries
}

func newAPIKeyHandler(db *db.Queries) *apiKeyHandler {
	return &apiKeyHandler{db: db}
}

type createAPIKeyRequest struct {
	Name string `json:"name"`
}

type apiKeyResponse struct {
	ID        string  `json:"id"`
	TeamID    string  `json:"team_id"`
	Name      string  `json:"name"`
	KeyPrefix string  `json:"key_prefix"`
	CreatedAt string  `json:"created_at"`
	LastUsed  *string `json:"last_used,omitempty"`
	Key       *string `json:"key,omitempty"` // only populated on Create
}

func apiKeyToResponse(k db.TeamApiKey) apiKeyResponse {
	resp := apiKeyResponse{
		ID:        k.ID,
		TeamID:    k.TeamID,
		Name:      k.Name,
		KeyPrefix: k.KeyPrefix,
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

	if req.Name == "" {
		req.Name = "Unnamed API Key"
	}

	plaintext, hash, err := auth.GenerateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate API key")
		return
	}

	keyID := id.NewAPIKeyID()
	row, err := h.db.InsertAPIKey(r.Context(), db.InsertAPIKeyParams{
		ID:        keyID,
		TeamID:    ac.TeamID,
		Name:      req.Name,
		KeyHash:   hash,
		KeyPrefix: auth.APIKeyPrefix(plaintext),
		CreatedBy: ac.UserID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to create API key")
		return
	}

	resp := apiKeyToResponse(row)
	resp.Key = &plaintext

	writeJSON(w, http.StatusCreated, resp)
}

// List handles GET /v1/api-keys.
func (h *apiKeyHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	keys, err := h.db.ListAPIKeysByTeam(r.Context(), ac.TeamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list API keys")
		return
	}

	resp := make([]apiKeyResponse, len(keys))
	for i, k := range keys {
		resp[i] = apiKeyToResponse(k)
	}

	writeJSON(w, http.StatusOK, resp)
}

// Delete handles DELETE /v1/api-keys/{id}.
func (h *apiKeyHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	keyID := chi.URLParam(r, "id")

	if err := h.db.DeleteAPIKey(r.Context(), db.DeleteAPIKeyParams{
		ID:     keyID,
		TeamID: ac.TeamID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to delete API key")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
