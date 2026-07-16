package api

import (
	"net/http"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

// requireHostToken validates the X-Host-Token header containing a host JWT,
// verifies the signature and expiry, and stamps HostContext into the request context.
func requireHostToken(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tokenStr := r.Header.Get("X-Host-Token")
			if tokenStr == "" {
				writeErr(w, r, apperr.Unauthorized.Msg("The X-Host-Token header is required."))
				return
			}

			claims, err := auth.VerifyHostJWT(secret, tokenStr)
			if err != nil {
				writeErr(w, r, apperr.Unauthorized.WrapMsg(err, "Host token is invalid or has expired."))
				return
			}

			hostID, err := id.ParseHostID(claims.HostID)
			if err != nil {
				writeErr(w, r, apperr.Unauthorized.WrapMsg(err, "Host token carries an invalid host ID."))
				return
			}

			ctx := auth.WithHostContext(r.Context(), auth.HostContext{HostID: hostID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
