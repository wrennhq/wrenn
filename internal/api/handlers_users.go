package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/auth"
	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/id"
	"git.omukk.dev/wrenn/wrenn/internal/service"
)

type usersHandler struct {
	db  *db.Queries
	svc *service.UserService
}

func newUsersHandler(db *db.Queries, svc *service.UserService) *usersHandler {
	return &usersHandler{db: db, svc: svc}
}

// Search handles GET /v1/users/search?email=<prefix>
// Returns up to 10 users whose email starts with the given prefix.
// The prefix must be at least 3 characters long and contain "@".
func (h *usersHandler) Search(w http.ResponseWriter, r *http.Request) {
	auth.MustFromContext(r.Context()) // ensure authenticated

	prefix := strings.TrimSpace(r.URL.Query().Get("email"))
	if len(prefix) < 3 || !strings.Contains(prefix, "@") {
		writeError(w, http.StatusBadRequest, "invalid_request", "email prefix must be at least 3 characters and contain '@'")
		return
	}

	// Escape LIKE metacharacters to prevent pattern injection.
	escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(prefix)

	results, err := h.db.SearchUsersByEmailPrefix(r.Context(), pgtype.Text{String: escaped, Valid: true})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "search failed")
		return
	}

	type userResult struct {
		UserID string `json:"user_id"`
		Email  string `json:"email"`
	}
	resp := make([]userResult, len(results))
	for i, u := range results {
		resp[i] = userResult{UserID: id.FormatUserID(u.ID), Email: u.Email}
	}
	writeJSON(w, http.StatusOK, resp)
}

// AdminListUsers handles GET /v1/admin/users?page=1
// Returns a paginated list of all users with team counts.
func (h *usersHandler) AdminListUsers(w http.ResponseWriter, r *http.Request) {
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		if _, err := fmt.Sscanf(p, "%d", &page); err != nil || page < 1 {
			page = 1
		}
	}
	const perPage = 100
	offset := int32((page - 1) * perPage)

	users, total, err := h.svc.AdminListUsers(r.Context(), perPage, offset)
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	type adminUserResponse struct {
		ID          string `json:"id"`
		Email       string `json:"email"`
		Name        string `json:"name"`
		IsAdmin     bool   `json:"is_admin"`
		IsActive    bool   `json:"is_active"`
		CreatedAt   string `json:"created_at"`
		TeamsJoined int32  `json:"teams_joined"`
		TeamsOwned  int32  `json:"teams_owned"`
	}

	resp := make([]adminUserResponse, len(users))
	for i, u := range users {
		resp[i] = adminUserResponse{
			ID:          id.FormatUserID(u.ID),
			Email:       u.Email,
			Name:        u.Name,
			IsAdmin:     u.IsAdmin,
			IsActive:    u.IsActive,
			CreatedAt:   u.CreatedAt.Format(time.RFC3339),
			TeamsJoined: u.TeamsJoined,
			TeamsOwned:  u.TeamsOwned,
		}
	}

	totalPages := (total + perPage - 1) / perPage
	writeJSON(w, http.StatusOK, map[string]any{
		"users":       resp,
		"total":       total,
		"page":        page,
		"per_page":    perPage,
		"total_pages": totalPages,
	})
}

// SetUserActive handles PUT /v1/admin/users/{id}/active
// Enables or disables a user account. Admins cannot deactivate themselves.
func (h *usersHandler) SetUserActive(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	userIDStr := chi.URLParam(r, "id")

	userID, err := id.ParseUserID(userIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid user ID")
		return
	}

	var req struct {
		Active bool `json:"active"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if ac.UserID == userID && !req.Active {
		writeError(w, http.StatusBadRequest, "invalid_request", "cannot deactivate your own account")
		return
	}

	if err := h.svc.SetUserActive(r.Context(), userID, req.Active); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
