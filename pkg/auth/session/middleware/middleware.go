// Package middleware exposes the session/CSRF middleware and cookie helpers
// that gate the browser-facing control plane API. It is the single source of
// truth — both internal/api and cloud extensions call into this package so
// auth semantics never diverge.
package middleware

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/session"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/netutil"
)

// Cookie + header names. Exported so extensions and frontends can reference
// the canonical values instead of hardcoding strings.
const (
	SessionCookieName = "wrenn_sid"
	CSRFCookieName    = "wrenn_csrf"
	CSRFHeaderName    = "X-CSRF-Token"
)

// IsSecure reports whether the inbound request should produce Secure cookies.
// Honors X-Forwarded-Proto for deployments behind TLS-terminating proxies.
func IsSecure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// SetCookies writes both the opaque session-id cookie (HttpOnly) and the
// JS-readable CSRF cookie used for double-submit validation.
func SetCookies(w http.ResponseWriter, sid, csrfToken string, secure bool) {
	maxAge := int(session.AbsoluteCap.Seconds())
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sid,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     CSRFCookieName,
		Value:    csrfToken,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: false,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	})
}

// ClearCookies invalidates the session and CSRF cookies on the response.
func ClearCookies(w http.ResponseWriter, secure bool) {
	for _, name := range []string{SessionCookieName, CSRFCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == SessionCookieName,
			SameSite: http.SameSiteStrictMode,
			Secure:   secure,
		})
	}
}

// ResolveSession reads the session cookie and returns the hydrated session,
// or session.ErrNotFound / session.ErrExpired on failure.
func ResolveSession(ctx context.Context, queries *db.Queries, svc *session.Service, r *http.Request) (*session.Session, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, session.ErrNotFound
	}
	return svc.Get(ctx, cookie.Value, hydrateFromDB(queries))
}

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
		} else if errors.Is(err, pgx.ErrNoRows) {
			// The session's active team no longer lists this user as a member
			// (e.g. they were removed from the team). Strip the team binding so
			// the stale session can no longer authorize against team-scoped
			// resources such as GetSandboxByTeam.
			sess.TeamID = pgtype.UUID{}
		} else {
			return err
		}
		sess.Email = user.Email
		sess.Name = user.Name
		sess.Role = role
		sess.IsAdmin = user.IsAdmin
		return nil
	}
}

// AuthContextFromSession builds the AuthContext middleware stamps into the
// request context after a successful session lookup.
func AuthContextFromSession(sess *session.Session) auth.AuthContext {
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

// AuthenticateAPIKey validates an X-API-Key value and returns a request
// context carrying the API-key-scoped AuthContext on success.
func AuthenticateAPIKey(ctx context.Context, queries *db.Queries, key, ip string) (context.Context, bool) {
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

// RequireSession returns middleware that allows only requests carrying a
// valid session cookie. On failure it clears stale cookies and responds 401.
func RequireSession(svc *session.Service, queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess, err := ResolveSession(r.Context(), queries, svc, r)
			if err != nil {
				ClearCookies(w, IsSecure(r))
				apperr.WriteHTTP(w, r, apperr.AuthSessionRequired.WrapMsg(err, "A valid session is required."))
				return
			}
			ctx := auth.WithAuthContext(r.Context(), AuthContextFromSession(sess))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireSessionOrAPIKey accepts X-API-Key (SDK) or wrenn_sid cookie (browser).
func RequireSessionOrAPIKey(svc *session.Service, queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if key := r.Header.Get("X-API-Key"); key != "" {
				if ctx, ok := AuthenticateAPIKey(r.Context(), queries, key, r.RemoteAddr); ok {
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				apperr.WriteHTTP(w, r, apperr.AuthInvalidAPIKey.New())
				return
			}
			sess, err := ResolveSession(r.Context(), queries, svc, r)
			if err != nil {
				ClearCookies(w, IsSecure(r))
				apperr.WriteHTTP(w, r, apperr.AuthSessionRequired.Wrap(err))
				return
			}
			ctx := auth.WithAuthContext(r.Context(), AuthContextFromSession(sess))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin enforces that the authenticated user is a platform admin.
// Must run after RequireSession. Re-reads is_admin from Postgres so a freshly
// revoked admin loses access on the next request — the cached session blob is
// only used for UI hints, never authorization.
func RequireAdmin(queries *db.Queries) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ac, ok := auth.FromContext(r.Context())
			if !ok {
				apperr.WriteHTTP(w, r, apperr.Unauthorized.New())
				return
			}
			user, err := queries.GetUserByID(r.Context(), ac.UserID)
			if err != nil || !user.IsAdmin {
				apperr.WriteHTTP(w, r, apperr.Forbidden.WrapMsg(err, "Admin access required."))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireCSRF returns middleware enforcing double-submit CSRF: the wrenn_csrf
// cookie value must equal the X-CSRF-Token header. Skipped for safe methods
// (GET/HEAD/OPTIONS) and for requests authenticated via X-API-Key.
func RequireCSRF() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if ac, ok := auth.FromContext(r.Context()); ok && ac.APIKeyID.Valid {
				next.ServeHTTP(w, r)
				return
			}
			cookie, err := r.Cookie(CSRFCookieName)
			header := r.Header.Get(CSRFHeaderName)
			if err != nil || cookie.Value == "" || header == "" ||
				subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
				apperr.WriteHTTP(w, r, apperr.AuthCSRF.New())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// IssueSession looks up identity columns, creates a fresh session, and writes
// the cookies onto the response. Intended for extension flows (invite-accept,
// admin impersonation, etc.) that need to log a user in without re-implementing
// the cookie wire-up.
func IssueSession(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	queries *db.Queries,
	svc *session.Service,
	userID, teamID pgtype.UUID,
) (*session.Session, error) {
	user, err := queries.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	role := ""
	membership, err := queries.GetTeamMembership(ctx, db.GetTeamMembershipParams{UserID: userID, TeamID: teamID})
	if err == nil {
		role = membership.Role
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	sess, err := svc.Create(ctx, userID, teamID, user.Email, user.Name, role, user.IsAdmin, r.UserAgent(), netutil.ClientIP(r))
	if err != nil {
		return nil, err
	}
	SetCookies(w, sess.RawSID, sess.CSRFToken, IsSecure(r))
	return sess, nil
}

