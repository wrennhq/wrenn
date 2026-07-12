// Package netutil holds small HTTP/networking helpers shared across the
// control plane. It lives in pkg/ so both internal/api and the auth
// middleware (and cloud extensions) can import a single implementation.
package netutil

import (
	"net/http"
	"strings"
)

// ClientIP returns the request's apparent client IP, honoring
// X-Forwarded-For when behind a reverse proxy.
func ClientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	return r.RemoteAddr
}
