package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/session"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

const (
	sessionCookieName = "wrenn_sid"
	csrfCookieName    = "wrenn_csrf"
)

// resolveSession reads the session cookie, looks up the session via the
// session service (Redis fast path, Postgres fallback), and returns the
// hydrated session or an error.
func resolveSession(ctx context.Context, queries *db.Queries, svc *session.Service, r *http.Request) (*session.Session, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, session.ErrNotFound
	}
	return svc.Get(ctx, cookie.Value, hydrateFromDB(queries))
}

// hydrateFromDB returns a callback that fetches the identity columns
// (email, name, role, is_admin) when the session is loaded from Postgres
// on cache miss. It also enforces user status (active only).
func hydrateFromDB(queries *db.Queries) func(context.Context, *session.Session) error {
	return func(ctx context.Context, sess *session.Session) error {
		user, err := queries.GetUserByID(ctx, sess.UserID)
		if err != nil {
			return err
		}
		if user.Status != "active" {
			return errors.New("account not active")
		}
		membership, err := queries.GetTeamMembership(ctx, db.GetTeamMembershipParams{
			UserID: sess.UserID,
			TeamID: sess.TeamID,
		})
		role := ""
		if err == nil {
			role = membership.Role
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		sess.Email = user.Email
		sess.Name = user.Name
		sess.Role = role
		sess.IsAdmin = user.IsAdmin
		return nil
	}
}

// requireSession enforces a valid session cookie. Used for browser-only routes.
func requireSession(queries *db.Queries, svc *session.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess, err := resolveSession(r.Context(), queries, svc, r)
			if err != nil {
				clearSessionCookies(w, isSecure(r))
				writeError(w, http.StatusUnauthorized, "unauthorized", "valid session required")
				return
			}
			ctx := auth.WithAuthContext(r.Context(), authContextFromSession(sess))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// requireSessionOrAPIKey accepts either X-API-Key (SDK) or wrenn_sid cookie
// (browser). Replaces the old requireAPIKeyOrJWT.
func requireSessionOrAPIKey(queries *db.Queries, svc *session.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if key := r.Header.Get("X-API-Key"); key != "" {
				if ctx, ok := authenticateAPIKey(r.Context(), queries, key, r.RemoteAddr); ok {
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				writeError(w, http.StatusUnauthorized, "unauthorized", "invalid API key")
				return
			}
			sess, err := resolveSession(r.Context(), queries, svc, r)
			if err != nil {
				clearSessionCookies(w, isSecure(r))
				writeError(w, http.StatusUnauthorized, "unauthorized", "X-API-Key header or session cookie required")
				return
			}
			ctx := auth.WithAuthContext(r.Context(), authContextFromSession(sess))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// authenticateAPIKey validates an X-API-Key header and returns the request
// context populated with AuthContext. Updates the key's last_used timestamp
// best-effort.
func authenticateAPIKey(ctx context.Context, queries *db.Queries, key, ip string) (context.Context, bool) {
	hash := auth.HashAPIKey(key)
	row, err := queries.GetAPIKeyByHash(ctx, hash)
	if err != nil {
		slog.Warn("api key auth failed", "prefix", auth.APIKeyPrefix(key), "ip", ip)
		return ctx, false
	}
	if err := queries.UpdateAPIKeyLastUsed(ctx, row.ID); err != nil {
		slog.Warn("failed to update api key last_used", "key_id", id.FormatAPIKeyID(row.ID), "error", err)
	}
	return auth.WithAuthContext(ctx, auth.AuthContext{
		TeamID:     row.TeamID,
		APIKeyID:   row.ID,
		APIKeyName: row.Name,
	}), true
}

func authContextFromSession(sess *session.Session) auth.AuthContext {
	return auth.AuthContext{
		TeamID:    sess.TeamID,
		UserID:    sess.UserID,
		Email:     sess.Email,
		Name:      sess.Name,
		Role:      sess.Role,
		IsAdmin:   sess.IsAdmin,
		SessionID: sess.ID,
	}
}

// setSessionCookies writes both the session-id cookie (HttpOnly) and the
// CSRF cookie (JS-readable, used for double-submit). MaxAge is the absolute
// session lifetime (24h); the browser will keep them across tab restarts.
func setSessionCookies(w http.ResponseWriter, sid, csrfToken string, secure bool) {
	maxAge := int(session.AbsoluteCap.Seconds())
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sid,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: false, // readable by JS for X-CSRF-Token double-submit
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	})
}

func clearSessionCookies(w http.ResponseWriter, secure bool) {
	for _, name := range []string{sessionCookieName, csrfCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == sessionCookieName,
			SameSite: http.SameSiteStrictMode,
			Secure:   secure,
		})
	}
}
