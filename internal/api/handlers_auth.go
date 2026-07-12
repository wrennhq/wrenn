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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/internal/email"
	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/session"
	"git.omukk.dev/wrenn/wrenn/pkg/cpextension"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/validate"
)

const (
	activationKeyPrefix = "wrenn:activation:"
	activationTTL       = 30 * time.Minute
	signupCooldown      = 30 * time.Minute
)

// loginTeam returns the team and role to stamp into a login JWT.
// It prefers the user's default team; if none is flagged as default it falls
// back to the earliest-joined team. Returns pgx.ErrNoRows when the user has
// no team memberships at all.
func loginTeam(ctx context.Context, q *db.Queries, userID pgtype.UUID) (db.Team, string, error) {
	team, err := q.GetDefaultTeamForUser(ctx, userID)
	if err == nil {
		membership, err := q.GetTeamMembership(ctx, db.GetTeamMembershipParams{UserID: userID, TeamID: team.ID})
		if err != nil {
			return db.Team{}, "", err
		}
		return team, membership.Role, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.Team{}, "", err
	}
	// No default set — fall back to earliest-joined team.
	rows, err := q.GetTeamsForUser(ctx, userID)
	if err != nil {
		return db.Team{}, "", err
	}
	if len(rows) == 0 {
		return db.Team{}, "", pgx.ErrNoRows
	}
	first := rows[0]
	return db.Team{
		ID:        first.ID,
		Name:      first.Name,
		Slug:      first.Slug,
		IsByoc:    first.IsByoc,
		CreatedAt: first.CreatedAt,
		DeletedAt: first.DeletedAt,
	}, first.Role, nil
}

// ensureDefaultTeam creates a default team for a user if they have none.
// This happens on first login after activation or for edge cases where a user
// has no teams. Returns the team, role, and whether the user was set as admin.
func ensureDefaultTeam(ctx context.Context, qtx *db.Queries, pool *pgxpool.Pool, userID pgtype.UUID, userName string) (db.Team, string, bool, error) {
	// Try existing teams first.
	team, role, err := loginTeam(ctx, qtx, userID)
	if err == nil {
		return team, role, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.Team{}, "", false, err
	}

	// No teams — create default team in a transaction.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return db.Team{}, "", false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	txq := qtx.WithTx(tx)

	// First active user to have a team becomes admin.
	activeCount, err := txq.CountActiveUsers(ctx)
	if err != nil {
		return db.Team{}, "", false, fmt.Errorf("count active users: %w", err)
	}
	isFirstUser := activeCount == 1 // only this user is active

	teamID := id.NewTeamID()
	teamRow, err := txq.InsertTeam(ctx, db.InsertTeamParams{
		ID:   teamID,
		Name: userName + "'s Team",
		Slug: id.NewTeamSlug(),
	})
	if err != nil {
		return db.Team{}, "", false, fmt.Errorf("insert team: %w", err)
	}

	if err := txq.InsertTeamMember(ctx, db.InsertTeamMemberParams{
		UserID:    userID,
		TeamID:    teamID,
		IsDefault: true,
		Role:      "owner",
	}); err != nil {
		return db.Team{}, "", false, fmt.Errorf("insert team member: %w", err)
	}

	if isFirstUser {
		if err := txq.SetUserAdmin(ctx, db.SetUserAdminParams{ID: userID, IsAdmin: true}); err != nil {
			return db.Team{}, "", false, fmt.Errorf("set admin: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return db.Team{}, "", false, fmt.Errorf("commit: %w", err)
	}

	return db.Team{
		ID:        teamRow.ID,
		Name:      teamRow.Name,
		Slug:      teamRow.Slug,
		IsByoc:    teamRow.IsByoc,
		CreatedAt: teamRow.CreatedAt,
		DeletedAt: teamRow.DeletedAt,
	}, "owner", isFirstUser, nil
}

type switchTeamRequest struct {
	TeamID string `json:"team_id"`
}

type authHandler struct {
	db          *db.Queries
	pool        *pgxpool.Pool
	sessions    *session.Service
	mailer      email.Mailer
	rdb         *redis.Client
	redirectURL string
	authHooks   []cpextension.AuthHook
}

func newAuthHandler(db *db.Queries, pool *pgxpool.Pool, sessions *session.Service, mailer email.Mailer, rdb *redis.Client, redirectURL string, hooks []cpextension.AuthHook) *authHandler {
	return &authHandler{db: db, pool: pool, sessions: sessions, mailer: mailer, rdb: rdb, redirectURL: strings.TrimRight(redirectURL, "/"), authHooks: hooks}
}

type signupRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type activateRequest struct {
	Token string `json:"token"`
}

type authResponse struct {
	UserID  string `json:"user_id"`
	TeamID  string `json:"team_id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Role    string `json:"role"`
	IsAdmin bool   `json:"is_admin"`
}

// issueSession creates a new session and writes both the session and CSRF
// cookies to the response. On success it writes the authResponse JSON body.
func (h *authHandler) issueSession(
	w http.ResponseWriter,
	r *http.Request,
	userID, teamID pgtype.UUID,
	email, name, role string,
	isAdmin bool,
) error {
	sess, err := h.sessions.Create(r.Context(), userID, teamID, email, name, role, isAdmin, r.UserAgent(), clientIP(r))
	if err != nil {
		return err
	}
	setSessionCookies(w, sess.RawSID, sess.CSRFToken, isSecure(r))
	return nil
}

// clientIP returns the request's apparent client IP, honoring
// X-Forwarded-For when behind a reverse proxy.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if i := strings.IndexByte(fwd, ','); i > 0 {
			return strings.TrimSpace(fwd[:i])
		}
		return strings.TrimSpace(fwd)
	}
	return r.RemoteAddr
}

type signupResponse struct {
	Message string `json:"message"`
}

// Signup handles POST /v1/auth/signup.
func (h *authHandler) Signup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Name = strings.TrimSpace(req.Name)
	if err := validate.Email(req.Email); err != nil {
		writeErr(w, r, apperr.ValidationFailed.WrapMsg(err, "A valid email address is required.").With("field", "email"))
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, r, apperr.ValidationFailed.Msg("Password must be at least 8 characters.").With("field", "password"))
		return
	}
	if err := validate.DisplayName(req.Name); err != nil {
		writeErr(w, r, apperr.ValidationFailed.WrapMsg(err, "Name may only contain letters, numbers, spaces, and . _ - (max 100 characters).").With("field", "name"))
		return
	}

	ctx := r.Context()

	// Check for existing user with this email.
	existing, err := h.db.GetUserByEmail(ctx, req.Email)
	if err == nil {
		// User exists — decide what to do based on status.
		switch existing.Status {
		case "inactive":
			// Unactivated user — allow re-signup after cooldown.
			if time.Since(existing.CreatedAt.Time) < signupCooldown {
				writeErr(w, r, apperr.AuthSignupCooldown.New())
				return
			}
			// Cooldown passed — delete the old row and proceed with fresh signup.
			if err := h.db.HardDeleteUser(ctx, existing.ID); err != nil {
				writeErr(w, r, apperr.Internal.Wrap(err))
				return
			}
		default:
			// active, disabled, deleted — email is taken.
			writeErr(w, r, apperr.AuthEmailTaken.New())
			return
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	userID := id.NewUserID()
	_, err = h.db.InsertUserInactive(ctx, db.InsertUserInactiveParams{
		ID:           userID,
		Email:        req.Email,
		PasswordHash: pgtype.Text{String: passwordHash, Valid: true},
		Name:         req.Name,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeErr(w, r, apperr.AuthEmailTaken.Wrap(err))
			return
		}
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	// Generate activation token and store in Redis.
	rawToken := generateOpaqueToken()
	tokenHash := hashOpaqueToken(rawToken)
	redisKey := activationKeyPrefix + tokenHash

	if err := h.rdb.Set(ctx, redisKey, id.FormatUserID(userID), activationTTL).Err(); err != nil {
		slog.Error("signup: failed to store activation token in redis", "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	activateURL := h.redirectURL + "/activate?token=" + rawToken
	go func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := h.mailer.Send(sendCtx, req.Email, "Activate your Wrenn account", email.EmailData{
			RecipientName: req.Name,
			Message:       "Welcome to Wrenn! Click the button below to activate your account. This link expires in 30 minutes.",
			Button:        &email.Button{Text: "Activate Account", URL: activateURL},
			Closing:       "If you didn't create this account, you can safely ignore this email.",
		}); err != nil {
			slog.Warn("signup: failed to send activation email", "email", req.Email, "error", err)
		}
	}()

	writeJSON(w, http.StatusCreated, signupResponse{
		Message: "Account created. Please check your email to activate your account.",
	})
}

// Activate handles POST /v1/auth/activate.
func (h *authHandler) Activate(w http.ResponseWriter, r *http.Request) {
	var req activateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}

	if req.Token == "" {
		writeErr(w, r, apperr.ValidationFailed.Msg("The token field is required.").With("field", "token"))
		return
	}

	ctx := r.Context()
	tokenHash := hashOpaqueToken(req.Token)
	redisKey := activationKeyPrefix + tokenHash

	userIDStr, err := h.rdb.GetDel(ctx, redisKey).Result()
	if errors.Is(err, redis.Nil) {
		writeErr(w, r, apperr.AuthTokenInvalid.Wrap(err))
		return
	}
	if err != nil {
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	userID, err := id.ParseUserID(userIDStr)
	if err != nil {
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	user, err := h.db.GetUserByID(ctx, userID)
	if err != nil {
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	if user.Status != "inactive" {
		writeErr(w, r, apperr.AuthAlreadyActivated.New())
		return
	}

	// Activate the user.
	if err := h.db.SetUserStatus(ctx, db.SetUserStatusParams{
		ID:     userID,
		Status: "active",
	}); err != nil {
		slog.Error("activate: failed to set user status", "user_id", id.FormatUserID(userID), "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	// Create default team and log them in.
	team, role, isFirstUser, err := ensureDefaultTeam(ctx, h.db, h.pool, userID, user.Name)
	if err != nil {
		slog.Error("activate: failed to create default team", "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	isAdmin := user.IsAdmin || isFirstUser
	// Fire OnSignup before issuing a session — billing must succeed first.
	if err := fireOnSignup(ctx, h.authHooks, userID, team.ID, user.Email); err != nil {
		slog.Error("activate: OnSignup hook failed", "user_id", id.FormatUserID(userID), "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}
	if err := h.issueSession(w, r, userID, team.ID, user.Email, user.Name, role, isAdmin); err != nil {
		slog.Error("activate: failed to issue session", "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}
	fireOnLogin(ctx, h.authHooks, userID)

	writeJSON(w, http.StatusOK, authResponse{
		UserID:  id.FormatUserID(userID),
		TeamID:  id.FormatTeamID(team.ID),
		Email:   user.Email,
		Name:    user.Name,
		Role:    role,
		IsAdmin: isAdmin,
	})
}

// Login handles POST /v1/auth/login.
func (h *authHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		writeErr(w, r, apperr.ValidationFailed.Msg("Email and password are required."))
		return
	}

	ctx := r.Context()

	user, err := h.db.GetUserByEmail(ctx, req.Email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("login failed: unknown email", "email", req.Email, "ip", r.RemoteAddr)
			writeErr(w, r, apperr.AuthInvalidCredentials.New())
			return
		}
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	if !user.PasswordHash.Valid {
		slog.Warn("login failed: no password set", "email", req.Email, "ip", r.RemoteAddr)
		writeErr(w, r, apperr.AuthInvalidCredentials.New())
		return
	}
	if err := auth.CheckPassword(user.PasswordHash.String, req.Password); err != nil {
		slog.Warn("login failed: wrong password", "email", req.Email, "ip", r.RemoteAddr)
		writeErr(w, r, apperr.AuthInvalidCredentials.New())
		return
	}

	switch user.Status {
	case "active":
		// OK — proceed.
	case "inactive":
		slog.Warn("login failed: account not activated", "email", req.Email, "ip", r.RemoteAddr)
		writeErr(w, r, apperr.AuthAccountNotActivated.New())
		return
	case "disabled":
		slog.Warn("login failed: account disabled", "email", req.Email, "ip", r.RemoteAddr)
		writeErr(w, r, apperr.AuthAccountDisabled.New())
		return
	case "deleted":
		slog.Warn("login failed: account deleted", "email", req.Email, "ip", r.RemoteAddr)
		writeErr(w, r, apperr.AuthInvalidCredentials.New())
		return
	default:
		writeErr(w, r, apperr.AuthInvalidCredentials.New())
		return
	}

	// Ensure user has a default team (creates one on first login after activation).
	team, role, isFirstUser, err := ensureDefaultTeam(ctx, h.db, h.pool, user.ID, user.Name)
	if err != nil {
		slog.Error("login: failed to ensure default team", "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	isAdmin := user.IsAdmin || isFirstUser
	if err := h.issueSession(w, r, user.ID, team.ID, user.Email, user.Name, role, isAdmin); err != nil {
		slog.Error("login: failed to issue session", "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}
	fireOnLogin(ctx, h.authHooks, user.ID)

	writeJSON(w, http.StatusOK, authResponse{
		UserID:  id.FormatUserID(user.ID),
		TeamID:  id.FormatTeamID(team.ID),
		Email:   user.Email,
		Name:    user.Name,
		Role:    role,
		IsAdmin: isAdmin,
	})
}

// SwitchTeam handles POST /v1/auth/switch-team.
// Verifies from DB that the user is a member of the target team, then re-issues
// a JWT scoped to that team. The JWT's team_id is used as a pre-filter on all
// subsequent team-scoped requests; DB is the source of truth for actual permissions.
func (h *authHandler) SwitchTeam(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	var req switchTeamRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}
	if req.TeamID == "" {
		writeErr(w, r, apperr.ValidationFailed.Msg("The team_id field is required.").With("field", "team_id"))
		return
	}

	teamID, err := id.ParseTeamID(req.TeamID)
	if err != nil {
		writeErr(w, r, apperr.ValidationFailed.WrapMsg(err, "The team_id field is invalid.").With("field", "team_id"))
		return
	}

	ctx := r.Context()

	// Verify team exists and is not deleted.
	team, err := h.db.GetTeam(ctx, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, apperr.TeamNotFound.Wrap(err))
			return
		}
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}
	if team.DeletedAt.Valid {
		writeErr(w, r, apperr.TeamNotFound.New())
		return
	}

	// Verify membership from DB — JWT role is not trusted here.
	membership, err := h.db.GetTeamMembership(ctx, db.GetTeamMembershipParams{
		UserID: ac.UserID,
		TeamID: teamID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, r, apperr.Forbidden.WrapMsg(err, "You are not a member of this team."))
			return
		}
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	// Fetch current name from DB — JWT name is not trusted here (may be stale or empty for old tokens).
	user, err := h.db.GetUserByID(ctx, ac.UserID)
	if err != nil {
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	// Rotate the SID so any leaked old cookie loses access at the moment of
	// privilege change.
	newSess, err := h.sessions.Rotate(ctx, ac.SessionID, ac.UserID, teamID, user.Email, user.Name, membership.Role, user.IsAdmin, r.UserAgent(), clientIP(r))
	if err != nil {
		slog.Error("switch team: failed to rotate session", "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}
	setSessionCookies(w, newSess.RawSID, newSess.CSRFToken, isSecure(r))

	writeJSON(w, http.StatusOK, authResponse{
		UserID:  id.FormatUserID(ac.UserID),
		TeamID:  id.FormatTeamID(teamID),
		Email:   user.Email,
		Name:    user.Name,
		Role:    membership.Role,
		IsAdmin: user.IsAdmin,
	})
}

// Logout handles POST /v1/auth/logout — revokes the caller's current session.
func (h *authHandler) Logout(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	if err := h.sessions.Revoke(r.Context(), ac.SessionID); err != nil {
		slog.Warn("logout: revoke failed", "error", err)
	}
	clearSessionCookies(w, isSecure(r))
	w.WriteHeader(http.StatusNoContent)
}

// LogoutAll handles POST /v1/auth/logout-all — revokes every session for the
// current user, including the caller's own.
func (h *authHandler) LogoutAll(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	if err := h.sessions.RevokeAllForUser(r.Context(), ac.UserID); err != nil {
		slog.Error("logout-all: revoke failed", "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}
	clearSessionCookies(w, isSecure(r))
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

// generateOpaqueToken returns a fresh 16-byte hex-encoded random token,
// used for email activation links and password reset links.
func generateOpaqueToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// hashOpaqueToken returns the SHA-256 hex digest of raw, used as the
// lookup key for one-shot tokens stored in Redis.
func hashOpaqueToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
