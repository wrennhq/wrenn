package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/oauth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

type oauthHandler struct {
	db          *db.Queries
	pool        *pgxpool.Pool
	jwtSecret   []byte
	registry    *oauth.Registry
	redirectURL string // base frontend URL (e.g. "https://app.wrenn.dev")
}

func newOAuthHandler(db *db.Queries, pool *pgxpool.Pool, jwtSecret []byte, registry *oauth.Registry, redirectURL string) *oauthHandler {
	return &oauthHandler{
		db:          db,
		pool:        pool,
		jwtSecret:   jwtSecret,
		registry:    registry,
		redirectURL: strings.TrimRight(redirectURL, "/"),
	}
}

// Redirect handles GET /v1/auth/oauth/{provider} — redirects to the provider's authorization page.
func (h *oauthHandler) Redirect(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")
	p, ok := h.registry.Get(provider)
	if !ok {
		writeError(w, http.StatusNotFound, "provider_not_found", "unsupported OAuth provider")
		return
	}

	state, err := generateState()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate state")
		return
	}

	mac := computeHMAC(h.jwtSecret, state)
	cookieVal := state + ":" + mac

	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    cookieVal,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
	})

	http.Redirect(w, r, p.AuthCodeURL(state), http.StatusFound)
}

// Callback handles GET /v1/auth/oauth/{provider}/callback — exchanges the code and logs in or registers the user.
func (h *oauthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")
	p, ok := h.registry.Get(provider)
	if !ok {
		writeError(w, http.StatusNotFound, "provider_not_found", "unsupported OAuth provider")
		return
	}

	redirectBase := h.redirectURL + "/auth/" + provider + "/callback"

	// Check if the provider returned an error.
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		redirectWithError(w, r, redirectBase, "access_denied")
		return
	}

	// Validate CSRF state.
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil {
		redirectWithError(w, r, redirectBase, "invalid_state")
		return
	}
	// Expire the state cookie immediately.
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
	})

	parts := strings.SplitN(stateCookie.Value, ":", 2)
	if len(parts) != 2 {
		redirectWithError(w, r, redirectBase, "invalid_state")
		return
	}
	nonce, expectedMAC := parts[0], parts[1]
	if !hmac.Equal([]byte(computeHMAC(h.jwtSecret, nonce)), []byte(expectedMAC)) {
		redirectWithError(w, r, redirectBase, "invalid_state")
		return
	}
	if r.URL.Query().Get("state") != nonce {
		redirectWithError(w, r, redirectBase, "invalid_state")
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		redirectWithError(w, r, redirectBase, "missing_code")
		return
	}

	// Exchange authorization code for user profile.
	ctx := r.Context()
	profile, err := p.Exchange(ctx, code)
	if err != nil {
		slog.Error("oauth exchange failed", "provider", provider, "error", err)
		redirectWithError(w, r, redirectBase, "exchange_failed")
		return
	}

	email := strings.TrimSpace(strings.ToLower(profile.Email))

	// Check for a link operation initiated from the settings page.
	if linkCookie, err := r.Cookie("oauth_link_user_id"); err == nil && linkCookie.Value != "" {
		// Clear the link cookie immediately.
		http.SetCookie(w, &http.Cookie{
			Name:     "oauth_link_user_id",
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   isSecure(r),
		})

		settingsBase := h.redirectURL + "/dashboard/settings"

		// Verify the HMAC to prevent cookie forgery.
		linkParts := strings.SplitN(linkCookie.Value, ":", 2)
		if len(linkParts) != 2 || !hmac.Equal([]byte(computeHMAC(h.jwtSecret, linkParts[0])), []byte(linkParts[1])) {
			slog.Warn("oauth link: invalid or tampered link cookie")
			http.Redirect(w, r, settingsBase+"?connect_error=invalid_state", http.StatusFound)
			return
		}

		userID, parseErr := id.ParseUserID(linkParts[0])
		if parseErr != nil {
			slog.Error("oauth link: invalid user ID in cookie", "error", parseErr)
			http.Redirect(w, r, settingsBase+"?connect_error=invalid_state", http.StatusFound)
			return
		}

		// Ensure the GitHub account isn't already linked to a different user.
		existing, lookupErr := h.db.GetOAuthProvider(ctx, db.GetOAuthProviderParams{
			Provider:   provider,
			ProviderID: profile.ProviderID,
		})
		if lookupErr == nil && existing.UserID != userID {
			slog.Warn("oauth link: provider already linked to another account", "provider", provider)
			http.Redirect(w, r, settingsBase+"?connect_error=already_linked", http.StatusFound)
			return
		}
		if lookupErr == nil && existing.UserID == userID {
			// Already linked to this user — treat as success.
			http.Redirect(w, r, settingsBase+"?connected="+provider, http.StatusFound)
			return
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			slog.Error("oauth link: db lookup failed", "error", lookupErr)
			http.Redirect(w, r, settingsBase+"?connect_error=db_error", http.StatusFound)
			return
		}

		if insertErr := h.db.InsertOAuthProvider(ctx, db.InsertOAuthProviderParams{
			Provider:   provider,
			ProviderID: profile.ProviderID,
			UserID:     userID,
			Email:      email,
		}); insertErr != nil {
			slog.Error("oauth link: failed to insert provider", "error", insertErr)
			http.Redirect(w, r, settingsBase+"?connect_error=db_error", http.StatusFound)
			return
		}

		slog.Info("oauth link: provider linked", "provider", provider, "user_id", id.FormatUserID(userID))
		http.Redirect(w, r, settingsBase+"?connected="+provider, http.StatusFound)
		return
	}

	// Check if this OAuth identity already exists.
	existing, err := h.db.GetOAuthProvider(ctx, db.GetOAuthProviderParams{
		Provider:   provider,
		ProviderID: profile.ProviderID,
	})
	if err == nil {
		// Existing OAuth user — log them in.
		user, err := h.db.GetUserByID(ctx, existing.UserID)
		if err != nil {
			slog.Error("oauth login: failed to get user", "error", err)
			redirectWithError(w, r, redirectBase, "db_error")
			return
		}
		if !user.IsActive {
			slog.Warn("oauth login: account deactivated", "email", user.Email)
			redirectWithError(w, r, redirectBase, "account_deactivated")
			return
		}
		team, role, err := loginTeam(ctx, h.db, user.ID)
		if err != nil {
			slog.Error("oauth login: failed to get team", "error", err)
			redirectWithError(w, r, redirectBase, "db_error")
			return
		}
		token, err := auth.SignJWT(h.jwtSecret, user.ID, team.ID, user.Email, user.Name, role, user.IsAdmin)
		if err != nil {
			slog.Error("oauth login: failed to sign jwt", "error", err)
			redirectWithError(w, r, redirectBase, "internal_error")
			return
		}
		redirectWithToken(w, r, redirectBase, token, id.FormatUserID(user.ID), id.FormatTeamID(team.ID), user.Email, user.Name)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("oauth: db lookup failed", "error", err)
		redirectWithError(w, r, redirectBase, "db_error")
		return
	}

	// New OAuth identity — check for email collision.
	_, err = h.db.GetUserByEmail(ctx, email)
	if err == nil {
		// Email already taken by another account.
		redirectWithError(w, r, redirectBase, "email_taken")
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		slog.Error("oauth: email check failed", "error", err)
		redirectWithError(w, r, redirectBase, "db_error")
		return
	}

	// Register: create user + team + membership + oauth_provider atomically.
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("oauth: failed to begin tx", "error", err)
		redirectWithError(w, r, redirectBase, "db_error")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	qtx := h.db.WithTx(tx)

	// The first user to sign up becomes a platform admin.
	userCount, err := qtx.CountUsers(ctx)
	if err != nil {
		slog.Error("oauth: failed to count users", "error", err)
		redirectWithError(w, r, redirectBase, "db_error")
		return
	}
	isFirstUser := userCount == 0

	userID := id.NewUserID()
	_, err = qtx.InsertUserOAuth(ctx, db.InsertUserOAuthParams{
		ID:    userID,
		Email: email,
		Name:  profile.Name,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Race condition: another request just created this user.
			// Rollback and retry as a login.
			tx.Rollback(ctx) //nolint:errcheck
			h.retryAsLogin(w, r, provider, profile.ProviderID, redirectBase)
			return
		}
		slog.Error("oauth: failed to create user", "error", err)
		redirectWithError(w, r, redirectBase, "db_error")
		return
	}

	teamID := id.NewTeamID()
	teamName := profile.Name + "'s Team"
	if _, err := qtx.InsertTeam(ctx, db.InsertTeamParams{
		ID:   teamID,
		Name: teamName,
		Slug: id.NewTeamSlug(),
	}); err != nil {
		slog.Error("oauth: failed to create team", "error", err)
		redirectWithError(w, r, redirectBase, "db_error")
		return
	}

	if err := qtx.InsertTeamMember(ctx, db.InsertTeamMemberParams{
		UserID:    userID,
		TeamID:    teamID,
		IsDefault: true,
		Role:      "owner",
	}); err != nil {
		slog.Error("oauth: failed to add team member", "error", err)
		redirectWithError(w, r, redirectBase, "db_error")
		return
	}

	if isFirstUser {
		if err := qtx.SetUserAdmin(ctx, db.SetUserAdminParams{ID: userID, IsAdmin: true}); err != nil {
			slog.Error("oauth: failed to set admin status", "error", err)
			redirectWithError(w, r, redirectBase, "db_error")
			return
		}
	}

	if err := qtx.InsertOAuthProvider(ctx, db.InsertOAuthProviderParams{
		Provider:   provider,
		ProviderID: profile.ProviderID,
		UserID:     userID,
		Email:      email,
	}); err != nil {
		slog.Error("oauth: failed to save oauth provider", "error", err)
		redirectWithError(w, r, redirectBase, "db_error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("oauth: failed to commit", "error", err)
		redirectWithError(w, r, redirectBase, "db_error")
		return
	}

	token, err := auth.SignJWT(h.jwtSecret, userID, teamID, email, profile.Name, "owner", isFirstUser)
	if err != nil {
		slog.Error("oauth: failed to sign jwt", "error", err)
		redirectWithError(w, r, redirectBase, "internal_error")
		return
	}

	redirectWithToken(w, r, redirectBase, token, id.FormatUserID(userID), id.FormatTeamID(teamID), email, profile.Name)
}

// retryAsLogin handles the race where a concurrent request already created the user.
// It looks up the oauth_providers row and logs in the existing user.
func (h *oauthHandler) retryAsLogin(w http.ResponseWriter, r *http.Request, provider, providerID, redirectBase string) {
	ctx := r.Context()
	existing, err := h.db.GetOAuthProvider(ctx, db.GetOAuthProviderParams{
		Provider:   provider,
		ProviderID: providerID,
	})
	if err != nil {
		slog.Error("oauth: retry login failed", "error", err)
		redirectWithError(w, r, redirectBase, "email_taken")
		return
	}
	user, err := h.db.GetUserByID(ctx, existing.UserID)
	if err != nil {
		slog.Error("oauth: retry login: failed to get user", "error", err)
		redirectWithError(w, r, redirectBase, "db_error")
		return
	}
	if !user.IsActive {
		slog.Warn("oauth: retry login: account deactivated", "email", user.Email)
		redirectWithError(w, r, redirectBase, "account_deactivated")
		return
	}
	team, role, err := loginTeam(ctx, h.db, user.ID)
	if err != nil {
		slog.Error("oauth: retry login: failed to get team", "error", err)
		redirectWithError(w, r, redirectBase, "db_error")
		return
	}
	token, err := auth.SignJWT(h.jwtSecret, user.ID, team.ID, user.Email, user.Name, role, user.IsAdmin)
	if err != nil {
		slog.Error("oauth: retry login: failed to sign jwt", "error", err)
		redirectWithError(w, r, redirectBase, "internal_error")
		return
	}
	redirectWithToken(w, r, redirectBase, token, id.FormatUserID(user.ID), id.FormatTeamID(team.ID), user.Email, user.Name)
}

func redirectWithToken(w http.ResponseWriter, r *http.Request, base, token, userID, teamID, email, name string) {
	// Set auth data as short-lived cookies instead of URL query parameters.
	// This prevents token leakage via server access logs, Referer headers, and browser history.
	for _, c := range []http.Cookie{
		{Name: "wrenn_oauth_token", Value: token},
		{Name: "wrenn_oauth_user_id", Value: userID},
		{Name: "wrenn_oauth_team_id", Value: teamID},
		{Name: "wrenn_oauth_email", Value: email},
		{Name: "wrenn_oauth_name", Value: name},
	} {
		c.Path = "/auth/"
		c.MaxAge = 60
		c.HttpOnly = false // frontend JS must read these
		c.SameSite = http.SameSiteLaxMode
		c.Secure = isSecure(r)
		http.SetCookie(w, &c)
	}
	http.Redirect(w, r, base, http.StatusFound)
}

func redirectWithError(w http.ResponseWriter, r *http.Request, base, code string) {
	http.Redirect(w, r, base+"?error="+url.QueryEscape(code), http.StatusFound)
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func computeHMAC(key []byte, data string) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

func isSecure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
