package api

import (
	"net/http"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
)

// requireAdmin validates that the authenticated user is a platform admin.
// Must run after requireJWT (depends on AuthContext being present).
// Re-validates against the DB — the JWT is_admin claim is for UI only;
// the DB is the source of truth for admin access.
func requireAdmin(queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ac, ok := auth.FromContext(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
				return
			}
			user, err := queries.GetUserByID(r.Context(), ac.UserID)
			if err != nil || !user.IsAdmin {
				writeError(w, http.StatusForbidden, "forbidden", "admin access required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
