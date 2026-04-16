package api

import (
	"log/slog"
	"net/http"
	"strings"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

// requireJWT validates a JWT from the Authorization: Bearer header.
// It also verifies the user is still active in the database.
// WebSocket upgrade requests without an Authorization header are passed through
// — WS handlers authenticate via the first message after upgrade.
func requireJWT(secret []byte, queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var tokenStr string
			if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
				tokenStr = strings.TrimPrefix(header, "Bearer ")
			}
			if tokenStr == "" {
				// WebSocket upgrade requests may not have an Authorization header
				// (browsers cannot set custom headers on WS connections). Let them
				// through — the handler authenticates via the first WS message.
				if isWebSocketUpgrade(r) {
					next.ServeHTTP(w, r)
					return
				}
				writeError(w, http.StatusUnauthorized, "unauthorized", "Authorization: Bearer <token> required")
				return
			}
			claims, err := auth.VerifyJWT(secret, tokenStr)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
				return
			}

			teamID, err := id.ParseTeamID(claims.TeamID)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid team ID in token")
				return
			}
			userID, err := id.ParseUserID(claims.Subject)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid user ID in token")
				return
			}

			// Verify user is still active in the database.
			user, err := queries.GetUserByID(r.Context(), userID)
			if err != nil {
				slog.Warn("jwt auth: failed to look up user", "user_id", claims.Subject, "error", err)
				writeError(w, http.StatusUnauthorized, "unauthorized", "user not found")
				return
			}
			if user.Status != "active" {
				writeError(w, http.StatusForbidden, "account_deactivated", "your account has been deactivated — contact your administrator to regain access")
				return
			}

			ctx := auth.WithAuthContext(r.Context(), auth.AuthContext{
				TeamID:  teamID,
				UserID:  userID,
				Email:   claims.Email,
				Name:    claims.Name,
				Role:    claims.Role,
				IsAdmin: claims.IsAdmin,
			})

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
