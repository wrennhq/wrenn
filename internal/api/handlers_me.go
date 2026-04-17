package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/internal/email"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/oauth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
)

const (
	passwordResetKeyPrefix = "wrenn:password_reset:"
	passwordResetTTL       = 15 * time.Minute
)

type meHandler struct {
	db            *db.Queries
	pool          *pgxpool.Pool
	rdb           *redis.Client
	jwtSecret     []byte
	mailer        email.Mailer
	oauthRegistry *oauth.Registry
	redirectURL   string
	teamSvc       *service.TeamService
}

func newMeHandler(
	db *db.Queries,
	pool *pgxpool.Pool,
	rdb *redis.Client,
	jwtSecret []byte,
	mailer email.Mailer,
	registry *oauth.Registry,
	redirectURL string,
	teamSvc *service.TeamService,
) *meHandler {
	return &meHandler{
		db:            db,
		pool:          pool,
		rdb:           rdb,
		jwtSecret:     jwtSecret,
		mailer:        mailer,
		oauthRegistry: registry,
		redirectURL:   strings.TrimRight(redirectURL, "/"),
		teamSvc:       teamSvc,
	}
}

type meResponse struct {
	Name        string   `json:"name"`
	Email       string   `json:"email"`
	HasPassword bool     `json:"has_password"`
	Providers   []string `json:"providers"`
}

type updateNameRequest struct {
	Name string `json:"name"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

type requestPasswordResetRequest struct {
	Email string `json:"email"`
}

type confirmPasswordResetRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

type deleteAccountRequest struct {
	Confirmation string `json:"confirmation"`
}

// GetMe handles GET /v1/me.
func (h *meHandler) GetMe(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	ctx := r.Context()

	user, err := h.db.GetUserByID(ctx, ac.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to get user")
		return
	}

	providers, err := h.db.GetOAuthProvidersByUserID(ctx, ac.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to get providers")
		return
	}

	providerNames := make([]string, 0, len(providers))
	for _, p := range providers {
		providerNames = append(providerNames, p.Provider)
	}

	writeJSON(w, http.StatusOK, meResponse{
		Name:        user.Name,
		Email:       user.Email,
		HasPassword: user.PasswordHash.Valid,
		Providers:   providerNames,
	})
}

// UpdateName handles PATCH /v1/me — updates the user's name and re-issues a JWT.
func (h *meHandler) UpdateName(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	ctx := r.Context()

	var req updateNameRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_request", "name must be between 1 and 100 characters")
		return
	}

	if err := h.db.UpdateUserName(ctx, db.UpdateUserNameParams{
		ID:   ac.UserID,
		Name: req.Name,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to update name")
		return
	}

	user, err := h.db.GetUserByID(ctx, ac.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to get user")
		return
	}

	team, role, err := loginTeam(ctx, h.db, ac.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to get team")
		return
	}

	token, err := auth.SignJWT(h.jwtSecret, ac.UserID, team.ID, user.Email, req.Name, role, user.IsAdmin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, authResponse{
		Token:  token,
		UserID: id.FormatUserID(ac.UserID),
		TeamID: id.FormatTeamID(team.ID),
		Email:  user.Email,
		Name:   req.Name,
	})
}

// ChangePassword handles POST /v1/me/password.
// For users with a password: requires current_password + new_password.
// For OAuth-only users: requires new_password + confirm_password.
func (h *meHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	ctx := r.Context()

	var req changePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	user, err := h.db.GetUserByID(ctx, ac.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to get user")
		return
	}

	if user.PasswordHash.Valid {
		// Changing existing password — verify current.
		if req.CurrentPassword == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "current_password is required")
			return
		}
		if err := auth.CheckPassword(user.PasswordHash.String, req.CurrentPassword); err != nil {
			writeError(w, http.StatusUnauthorized, "wrong_password", "current password is incorrect")
			return
		}
	} else {
		// OAuth user adding a password — confirm must match.
		if req.ConfirmPassword == "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "confirm_password is required")
			return
		}
		if req.NewPassword != req.ConfirmPassword {
			writeError(w, http.StatusBadRequest, "invalid_request", "passwords do not match")
			return
		}
	}

	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "invalid_request", "password must be at least 8 characters")
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to hash password")
		return
	}

	if err := h.db.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		ID:           ac.UserID,
		PasswordHash: pgtype.Text{String: hash, Valid: true},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to update password")
		return
	}

	isAdding := !user.PasswordHash.Valid
	go func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		subject, message := "Your Wrenn password was changed", "Your account password was successfully updated. If you did not make this change, reset your password immediately."
		if isAdding {
			subject = "Password added to your Wrenn account"
			message = "A password has been added to your Wrenn account. You can now sign in with your email and password in addition to any connected OAuth providers."
		}
		if err := h.mailer.Send(sendCtx, user.Email, subject, email.EmailData{
			RecipientName: user.Name,
			Message:       message,
			Closing:       "If you didn't make this change, contact support immediately.",
		}); err != nil {
			slog.Warn("change password: failed to send notification", "email", user.Email, "error", err)
		}
	}()

	w.WriteHeader(http.StatusNoContent)
}

// RequestPasswordReset handles POST /v1/me/password/reset (unauthenticated).
// Always returns 200 to avoid leaking account existence.
func (h *meHandler) RequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req requestPasswordResetRequest
	if err := decodeJSON(r, &req); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ctx := r.Context()

	user, err := h.db.GetUserByEmail(ctx, req.Email)
	if err != nil {
		// Don't leak whether the email exists.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if user.Status != "active" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	rawToken := generateResetToken()
	tokenHash := hashResetToken(rawToken)
	redisKey := passwordResetKeyPrefix + tokenHash

	if err := h.rdb.Set(ctx, redisKey, id.FormatUserID(user.ID), passwordResetTTL).Err(); err != nil {
		slog.Error("password reset: failed to store token in redis", "error", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	resetURL := h.redirectURL + "/reset-password?token=" + rawToken
	go func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := h.mailer.Send(sendCtx, user.Email, "Reset your Wrenn password", email.EmailData{
			RecipientName: user.Name,
			Message:       "We received a request to reset your password. Click the button below to set a new password. This link expires in 15 minutes.",
			Button:        &email.Button{Text: "Reset Password", URL: resetURL},
			Closing:       "If you didn't request a password reset, you can safely ignore this email.",
		}); err != nil {
			slog.Error("password reset: failed to send email", "email", user.Email, "error", err)
		}
	}()

	w.WriteHeader(http.StatusNoContent)
}

// ConfirmPasswordReset handles POST /v1/me/password/reset/confirm (unauthenticated).
func (h *meHandler) ConfirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req confirmPasswordResetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "invalid_request", "password must be at least 8 characters")
		return
	}

	ctx := r.Context()
	tokenHash := hashResetToken(req.Token)
	redisKey := passwordResetKeyPrefix + tokenHash

	// GetDel atomically retrieves and removes the token in a single round-trip,
	// preventing concurrent requests from both consuming the same token.
	userIDStr, err := h.rdb.GetDel(ctx, redisKey).Result()
	if errors.Is(err, redis.Nil) {
		writeError(w, http.StatusBadRequest, "invalid_token", "reset token is invalid or has expired")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to verify token")
		return
	}

	userID, err := id.ParseUserID(userIDStr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "invalid stored user ID")
		return
	}

	user, err := h.db.GetUserByID(ctx, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to get user")
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to hash password")
		return
	}

	if err := h.db.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		ID:           userID,
		PasswordHash: pgtype.Text{String: hash, Valid: true},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to update password")
		return
	}

	go func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := h.mailer.Send(sendCtx, user.Email, "Your Wrenn password was reset", email.EmailData{
			RecipientName: user.Name,
			Message:       "Your password has been successfully reset. You can now sign in with your new password.",
			Closing:       "If you didn't request this change, contact support immediately.",
		}); err != nil {
			slog.Warn("confirm password reset: failed to send notification", "email", user.Email, "error", err)
		}
	}()

	w.WriteHeader(http.StatusNoContent)
}

// ConnectProvider handles GET /v1/me/providers/{provider}/connect.
// Sets OAuth state + link cookies and returns the provider auth URL.
func (h *meHandler) ConnectProvider(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	provider := chi.URLParam(r, "provider")

	p, ok := h.oauthRegistry.Get(provider)
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
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state + ":" + mac,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
	})

	userIDStr := id.FormatUserID(ac.UserID)
	linkMac := computeHMAC(h.jwtSecret, userIDStr)
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_link_user_id",
		Value:    userIDStr + ":" + linkMac,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isSecure(r),
	})

	writeJSON(w, http.StatusOK, map[string]string{"auth_url": p.AuthCodeURL(state)})
}

// DisconnectProvider handles DELETE /v1/me/providers/{provider}.
func (h *meHandler) DisconnectProvider(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	provider := chi.URLParam(r, "provider")
	ctx := r.Context()

	user, err := h.db.GetUserByID(ctx, ac.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to get user")
		return
	}

	providers, err := h.db.GetOAuthProvidersByUserID(ctx, ac.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to get providers")
		return
	}

	// Ensure the user will still have at least one login method after disconnecting.
	if !user.PasswordHash.Valid && len(providers) <= 1 {
		writeError(w, http.StatusBadRequest, "last_login_method", "cannot disconnect your only login method — add a password first")
		return
	}

	// Check the provider is actually linked to this user.
	found := false
	for _, p := range providers {
		if p.Provider == provider {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "provider not connected")
		return
	}

	if err := h.db.DeleteOAuthProvider(ctx, db.DeleteOAuthProviderParams{
		UserID:   ac.UserID,
		Provider: provider,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to disconnect provider")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DeleteAccount handles DELETE /v1/me — soft-deletes the user's account.
func (h *meHandler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	ctx := r.Context()

	var req deleteAccountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	user, err := h.db.GetUserByID(ctx, ac.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to get user")
		return
	}

	if !strings.EqualFold(strings.TrimSpace(req.Confirmation), user.Email) {
		writeError(w, http.StatusBadRequest, "invalid_request", "confirmation does not match your email address")
		return
	}

	teamsBlocking, err := h.db.CountUserOwnedTeamsWithOtherMembers(ctx, ac.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to check team ownership")
		return
	}
	if teamsBlocking > 0 {
		writeError(w, http.StatusConflict, "owns_team_with_members",
			fmt.Sprintf("you own %d team(s) with other members — transfer ownership or remove members before deleting your account", teamsBlocking))
		return
	}

	// Delete all teams the user solely owns (no other members).
	// Team deletion involves RPC calls (sandbox destruction) that cannot be
	// transactional, so we do those first as best-effort, then wrap the
	// DB-only cleanup in a transaction.
	soleTeams, err := h.db.ListSoleOwnedTeams(ctx, ac.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list owned teams")
		return
	}
	for _, teamID := range soleTeams {
		if err := h.teamSvc.DeleteTeamInternal(ctx, teamID); err != nil {
			writeError(w, http.StatusInternalServerError, "db_error",
				fmt.Sprintf("failed to delete sole-owned team %s", id.FormatTeamID(teamID)))
			return
		}
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to start transaction")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	qtx := h.db.WithTx(tx)

	if err := qtx.DeleteAPIKeysByCreator(ctx, ac.UserID); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to delete user's API keys")
		return
	}

	if err := qtx.SoftDeleteUser(ctx, ac.UserID); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to delete account")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to commit account deletion")
		return
	}

	slog.Info("account soft-deleted", "user_id", id.FormatUserID(ac.UserID), "email", user.Email)

	go func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := h.mailer.Send(sendCtx, user.Email, "Your Wrenn account has been deleted", email.EmailData{
			RecipientName: user.Name,
			Message:       "Your Wrenn account has been deactivated and is scheduled for permanent deletion in 15 days. If this was a mistake, contact support before then to recover your account.",
			Closing:       "Thank you for using Wrenn.",
		}); err != nil {
			slog.Warn("delete account: failed to send notification", "email", user.Email, "error", err)
		}
	}()

	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func generateResetToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func hashResetToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
