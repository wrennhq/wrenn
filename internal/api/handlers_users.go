package api

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
)

type usersHandler struct {
	db *db.Queries
}

func newUsersHandler(db *db.Queries) *usersHandler {
	return &usersHandler{db: db}
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
		resp[i] = userResult{UserID: u.ID, Email: u.Email}
	}
	writeJSON(w, http.StatusOK, resp)
}
