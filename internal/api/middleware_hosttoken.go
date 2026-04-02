package api

import (
	"net/http"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/id"
)

// requireHostToken validates the X-Host-Token header containing a host JWT,
// verifies the signature and expiry, and stamps HostContext into the request context.
func requireHostToken(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := r.Header.Get("X-Host-Token")
			if tokenStr == "" {
				writeError(w, http.StatusUnauthorized, "unauthorized", "X-Host-Token header required")
				return
			}

			claims, err := auth.VerifyHostJWT(secret, tokenStr)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired host token")
				return
			}

			hostID, err := id.ParseHostID(claims.HostID)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid host ID in token")
				return
			}

			ctx := auth.WithHostContext(r.Context(), auth.HostContext{HostID: hostID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
