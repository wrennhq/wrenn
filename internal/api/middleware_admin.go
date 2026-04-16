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
				// No auth context yet (WS upgrade); handler will inject platform team after WS auth.
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

// requireAdmin validates that the authenticated user is a platform admin.
// Must run after requireJWT (depends on AuthContext being present).
// Re-validates against the DB — the JWT is_admin claim is for UI only;
// the DB is the source of truth for admin access.
// WebSocket upgrade requests without auth context are passed through —
// admin WS handlers verify admin status after upgrade via wsAuthenticateAdmin.
func requireAdmin(queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ac, ok := auth.FromContext(r.Context())
			if !ok {
				if isWebSocketUpgrade(r) {
					ctx := r.Context()
					ctx = setAdminWSFlag(ctx)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
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
