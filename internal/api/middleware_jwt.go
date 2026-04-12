package api

import (
	"net/http"
	"strings"

	"git.omukk.dev/wrenn/wrenn/internal/auth"
	"git.omukk.dev/wrenn/wrenn/internal/id"
)

// requireJWT validates a JWT from the Authorization: Bearer header or the
// ?token= query parameter (for WebSocket connections that cannot send headers).
func requireJWT(secret []byte) func(http.Handler) http.Handler {
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
