package api

import (
	"net/http"

	"git.omukk.dev/wrenn/wrenn/pkg/auth/session"
	sessionmw "git.omukk.dev/wrenn/wrenn/pkg/auth/session/middleware"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
)

// Internal aliases — the canonical implementations live in the public
// pkg/auth/session/middleware package so cloud extensions can call them.

const csrfCookieName = sessionmw.CSRFCookieName

func requireSession(queries *db.Queries, svc *session.Service) func(http.Handler) http.Handler {
	return sessionmw.RequireSession(svc, queries)
}

func requireSessionOrAPIKey(queries *db.Queries, svc *session.Service) func(http.Handler) http.Handler {
	return sessionmw.RequireSessionOrAPIKey(svc, queries)
}

func setSessionCookies(w http.ResponseWriter, sid, csrfToken string, secure bool) {
	sessionmw.SetCookies(w, sid, csrfToken, secure)
}

func clearSessionCookies(w http.ResponseWriter, secure bool) {
	sessionmw.ClearCookies(w, secure)
}

func isSecure(r *http.Request) bool {
	return sessionmw.IsSecure(r)
}
