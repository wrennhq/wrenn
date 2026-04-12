package api

import (
	"net/http"

	"git.omukk.dev/wrenn/wrenn/internal/auth"
	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/id"
)

// injectPlatformTeam overwrites the AuthContext's TeamID with the platform
// sentinel UUID. This lets existing team-scoped handlers (exec, files, pty,
// metrics) work unchanged under admin routes. Must run after requireAdmin.
func injectPlatformTeam() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ac := auth.MustFromContext(r.Context())
			ac.TeamID = id.PlatformTeamID
			ctx := auth.WithAuthContext(r.Context(), ac)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

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
