package api

import (
	"net/http"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

// injectPlatformTeam overwrites the AuthContext's TeamID with the platform
// sentinel UUID. This lets existing team-scoped handlers (exec, files, pty,
// metrics) work unchanged under admin routes. Must run after requireAdmin.
func injectPlatformTeam() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := auth.FromContext(r.Context()); !ok {
				next.ServeHTTP(w, r)
				return
			}
			ac := auth.MustFromContext(r.Context())
			ac.TeamID = id.PlatformTeamID
			ctx := auth.WithAuthContext(r.Context(), ac)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// markAdminWS flags the request context as an admin WebSocket route.
// Applied to admin WS endpoints that sit outside the requireJWT/requireAdmin
// middleware group. Handlers use isAdminWSRoute(ctx) to pick wsAuthenticateAdmin.
func markAdminWS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(setAdminWSFlag(r.Context())))
	})
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
