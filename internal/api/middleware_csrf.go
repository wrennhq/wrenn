package api

import (
	"crypto/subtle"
	"net/http"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
)

// requireCSRF enforces double-submit CSRF: the wrenn_csrf cookie value must
// equal the X-CSRF-Token header. Skipped for safe methods (GET/HEAD/OPTIONS)
// and for requests authenticated via X-API-Key (SDK clients are not
// vulnerable to cross-site request forgery against cookie auth).
func requireCSRF() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			// API-key auth path: no CSRF check needed.
			if ac, ok := auth.FromContext(r.Context()); ok && ac.APIKeyID.Valid {
				next.ServeHTTP(w, r)
				return
			}

			cookie, err := r.Cookie(csrfCookieName)
			header := r.Header.Get("X-CSRF-Token")
			if err != nil || cookie.Value == "" || header == "" ||
				subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
				writeError(w, http.StatusForbidden, "csrf_failed", "missing or invalid CSRF token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
