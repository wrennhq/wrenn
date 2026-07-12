package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/session"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
)

type usersHandler struct {
	db       *db.Queries
	svc      *service.UserService
	audit    *audit.AuditLogger
	sessions *session.Service
}

func newUsersHandler(db *db.Queries, svc *service.UserService, al *audit.AuditLogger, sessions *session.Service) *usersHandler {
	return &usersHandler{db: db, svc: svc, audit: al, sessions: sessions}
}

// Search handles GET /v1/users/search?email=<prefix>
// Returns up to 10 users whose email starts with the given prefix.
// The prefix must be at least 3 characters long and contain "@".
func (h *usersHandler) Search(w http.ResponseWriter, r *http.Request) {
	auth.MustFromContext(r.Context()) // ensure authenticated

	prefix := strings.TrimSpace(r.URL.Query().Get("email"))
	if len(prefix) < 3 || !strings.Contains(prefix, "@") {
		writeErr(w, r, apperr.ValidationFailed.Msg("The email prefix must be at least 3 characters and contain '@'.").With("field", "email"))
		return
	}

	// Escape LIKE metacharacters to prevent pattern injection.
	escaped := strings.NewReplacer("%", "\\%", "_", "\\_").Replace(prefix)

	results, err := h.db.SearchUsersByEmailPrefix(r.Context(), pgtype.Text{String: escaped, Valid: true})
	if err != nil {
		writeErr(w, r, apperr.Internal.Wrap(err))
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
		writeErr(w, r, err)
		return
	}

	type adminUserResponse struct {
		ID          string `json:"id"`
		Email       string `json:"email"`
		Name        string `json:"name"`
		IsAdmin     bool   `json:"is_admin"`
		Status      string `json:"status"`
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
			Status:      u.Status,
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
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid user ID."))
		return
	}

	var req struct {
		Active bool `json:"active"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}

	if ac.UserID == userID && !req.Active {
		writeErr(w, r, apperr.InvalidRequest.Msg("You cannot deactivate your own account."))
		return
	}

	newStatus := "active"
	if !req.Active {
		newStatus = "disabled"
	}

	// Look up user email for audit log before changing status.
	user, err := h.db.GetUserByID(r.Context(), userID)
	if err != nil {
		writeErr(w, r, apperr.UserNotFound.Wrap(err))
		return
	}

	if err := h.svc.SetUserStatus(r.Context(), userID, newStatus); err != nil {
		writeErr(w, r, err)
		return
	}

	if req.Active {
		h.audit.LogUserActivate(r.Context(), ac, userID, user.Email)
	} else {
		// Disabled users must be kicked out of every active session.
		if err := h.sessions.RevokeAllForUser(r.Context(), userID); err != nil {
			_ = err
		}
		h.audit.LogUserDeactivate(r.Context(), ac, userID, user.Email)
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetUserAdmin handles PUT /v1/admin/users/{id}/admin
// Grants or revokes platform admin status. Cannot remove the last admin.
func (h *usersHandler) SetUserAdmin(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	userIDStr := chi.URLParam(r, "id")

	userID, err := id.ParseUserID(userIDStr)
	if err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid user ID."))
		return
	}

	var req struct {
		Admin bool `json:"admin"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}

	user, err := h.db.GetUserByID(r.Context(), userID)
	if err != nil {
		writeErr(w, r, apperr.UserNotFound.Wrap(err))
		return
	}

	if user.IsAdmin == req.Admin {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if req.Admin {
		if err := h.db.SetUserAdmin(r.Context(), db.SetUserAdminParams{
			ID:      userID,
			IsAdmin: true,
		}); err != nil {
			writeErr(w, r, apperr.Internal.Wrap(err))
			return
		}
		h.audit.LogUserGrantAdmin(r.Context(), ac, userID, user.Email)
	} else {
		affected, err := h.db.RevokeUserAdmin(r.Context(), userID)
		if err != nil {
			writeErr(w, r, apperr.Internal.Wrap(err))
			return
		}
		if affected == 0 {
			writeErr(w, r, apperr.InvalidRequest.Msg("Cannot remove the last admin."))
			return
		}
		h.audit.LogUserRevokeAdmin(r.Context(), ac, userID, user.Email)
	}

	// Invalidate cached session blobs so the new is_admin flag is reflected
	// on the user's next request without waiting for the Redis TTL.
	if err := h.sessions.InvalidateCacheForUser(r.Context(), userID); err != nil {
		// Cache is best-effort; the DB is authoritative and requireAdmin
		// always re-reads it.
		_ = err
	}

	w.WriteHeader(http.StatusNoContent)
}
