package api

import (
	"log/slog"
	"net/http"
	"strings"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

// requireJWT validates a JWT from the Authorization: Bearer header or the
// ?token= query parameter (for WebSocket connections that cannot send headers).
// It also verifies the user is still active in the database.
func requireJWT(secret []byte, queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var tokenStr string
			if header := r.Header.Get("Authorization"); strings.HasPrefix(header, "Bearer ") {
				tokenStr = strings.TrimPrefix(header, "Bearer ")
			} else if t := r.URL.Query().Get("token"); t != "" {
				tokenStr = t
			}
			if tokenStr == "" {
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
