package api

import (
	"net/http"
	"strings"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
)

// requireJWT validates the Authorization: Bearer <token> header, verifies the JWT
// signature and expiry, and stamps UserID + TeamID + Email into the request context.
func requireJWT(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				writeError(w, http.StatusUnauthorized, "unauthorized", "Authorization: Bearer <token> required")
				return
			}

			tokenStr := strings.TrimPrefix(header, "Bearer ")
			claims, err := auth.VerifyJWT(secret, tokenStr)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
				return
			}

			ctx := auth.WithAuthContext(r.Context(), auth.AuthContext{
				TeamID:  claims.TeamID,
				UserID:  claims.Subject,
				Email:   claims.Email,
				Name:    claims.Name,
				Role:    claims.Role,
				IsAdmin: claims.IsAdmin,
			})

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
