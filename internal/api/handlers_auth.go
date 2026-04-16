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
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
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
	jwtSecret   []byte
	mailer      email.Mailer
	rdb         *redis.Client
	redirectURL string
}

func newAuthHandler(db *db.Queries, pool *pgxpool.Pool, jwtSecret []byte, mailer email.Mailer, rdb *redis.Client, redirectURL string) *authHandler {
	return &authHandler{db: db, pool: pool, jwtSecret: jwtSecret, mailer: mailer, rdb: rdb, redirectURL: strings.TrimRight(redirectURL, "/")}
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
	Token  string `json:"token"`
	UserID string `json:"user_id"`
	TeamID string `json:"team_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
}

type signupResponse struct {
	Message string `json:"message"`
}

// Signup handles POST /v1/auth/signup.
func (h *authHandler) Signup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Name = strings.TrimSpace(req.Name)
	if !strings.Contains(req.Email, "@") || len(req.Email) < 3 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid email address")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "invalid_request", "password must be at least 8 characters")
		return
	}
	if req.Name == "" || len(req.Name) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_request", "name must be between 1 and 100 characters")
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
				writeError(w, http.StatusConflict, "signup_cooldown",
					"an activation email was recently sent to this address — please check your inbox or try again later")
				return
			}
			// Cooldown passed — delete the old row and proceed with fresh signup.
			if err := h.db.HardDeleteUser(ctx, existing.ID); err != nil {
				writeError(w, http.StatusInternalServerError, "db_error", "failed to clean up previous signup")
				return
			}
		default:
			// active, disabled, deleted — email is taken.
			writeError(w, http.StatusConflict, "email_taken", "an account with this email already exists")
			return
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to look up user")
		return
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to hash password")
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
			writeError(w, http.StatusConflict, "email_taken", "an account with this email already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "failed to create user")
		return
	}

	// Generate activation token and store in Redis.
	rawToken := generateActivationToken()
	tokenHash := hashActivationToken(rawToken)
	redisKey := activationKeyPrefix + tokenHash

	if err := h.rdb.Set(ctx, redisKey, id.FormatUserID(userID), activationTTL).Err(); err != nil {
		slog.Error("signup: failed to store activation token in redis", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to create activation token")
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
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}

	ctx := r.Context()
	tokenHash := hashActivationToken(req.Token)
	redisKey := activationKeyPrefix + tokenHash

	userIDStr, err := h.rdb.GetDel(ctx, redisKey).Result()
	if errors.Is(err, redis.Nil) {
		writeError(w, http.StatusBadRequest, "invalid_token", "activation link is invalid or has expired")
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

	if user.Status != "inactive" {
		writeError(w, http.StatusBadRequest, "already_activated", "this account has already been activated")
		return
	}

	// Activate the user.
	if err := h.db.SetUserStatus(ctx, db.SetUserStatusParams{
		ID:     userID,
		Status: "active",
	}); err != nil {
		slog.Error("activate: failed to set user status", "user_id", id.FormatUserID(userID), "error", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to activate user")
		return
	}

	// Create default team and log them in.
	team, role, isFirstUser, err := ensureDefaultTeam(ctx, h.db, h.pool, userID, user.Name)
	if err != nil {
		slog.Error("activate: failed to create default team", "error", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to set up account")
		return
	}

	isAdmin := user.IsAdmin || isFirstUser
	token, err := auth.SignJWT(h.jwtSecret, userID, team.ID, user.Email, user.Name, role, isAdmin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, authResponse{
		Token:  token,
		UserID: id.FormatUserID(userID),
		TeamID: id.FormatTeamID(team.ID),
		Email:  user.Email,
		Name:   user.Name,
	})
}

// Login handles POST /v1/auth/login.
func (h *authHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "email and password are required")
		return
	}

	ctx := r.Context()

	user, err := h.db.GetUserByEmail(ctx, req.Email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("login failed: unknown email", "email", req.Email, "ip", r.RemoteAddr)
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid email or password")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "failed to look up user")
		return
	}

	if !user.PasswordHash.Valid {
		slog.Warn("login failed: no password set", "email", req.Email, "ip", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid email or password")
		return
	}
	if err := auth.CheckPassword(user.PasswordHash.String, req.Password); err != nil {
		slog.Warn("login failed: wrong password", "email", req.Email, "ip", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid email or password")
		return
	}

	switch user.Status {
	case "active":
		// OK — proceed.
	case "inactive":
		slog.Warn("login failed: account not activated", "email", req.Email, "ip", r.RemoteAddr)
		writeError(w, http.StatusForbidden, "account_not_activated", "please check your email and activate your account before signing in")
		return
	case "disabled":
		slog.Warn("login failed: account disabled", "email", req.Email, "ip", r.RemoteAddr)
		writeError(w, http.StatusForbidden, "account_disabled", "your account has been deactivated — contact your administrator to regain access")
		return
	case "deleted":
		slog.Warn("login failed: account deleted", "email", req.Email, "ip", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid email or password")
		return
	default:
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid email or password")
		return
	}

	// Ensure user has a default team (creates one on first login after activation).
	team, role, isFirstUser, err := ensureDefaultTeam(ctx, h.db, h.pool, user.ID, user.Name)
	if err != nil {
		slog.Error("login: failed to ensure default team", "error", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to look up team")
		return
	}

	isAdmin := user.IsAdmin || isFirstUser
	token, err := auth.SignJWT(h.jwtSecret, user.ID, team.ID, user.Email, user.Name, role, isAdmin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, authResponse{
		Token:  token,
		UserID: id.FormatUserID(user.ID),
		TeamID: id.FormatTeamID(team.ID),
		Email:  user.Email,
		Name:   user.Name,
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
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.TeamID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "team_id is required")
		return
	}

	teamID, err := id.ParseTeamID(req.TeamID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid team_id")
		return
	}

	ctx := r.Context()

	// Verify team exists and is not deleted.
	team, err := h.db.GetTeam(ctx, teamID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "failed to look up team")
		return
	}
	if team.DeletedAt.Valid {
		writeError(w, http.StatusNotFound, "not_found", "team not found")
		return
	}

	// Verify membership from DB — JWT role is not trusted here.
	membership, err := h.db.GetTeamMembership(ctx, db.GetTeamMembershipParams{
		UserID: ac.UserID,
		TeamID: teamID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusForbidden, "forbidden", "not a member of this team")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "failed to look up membership")
		return
	}

	// Fetch current name from DB — JWT name is not trusted here (may be stale or empty for old tokens).
	user, err := h.db.GetUserByID(ctx, ac.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to look up user")
		return
	}

	token, err := auth.SignJWT(h.jwtSecret, ac.UserID, teamID, ac.Email, user.Name, membership.Role, user.IsAdmin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, authResponse{
		Token:  token,
		UserID: id.FormatUserID(ac.UserID),
		TeamID: id.FormatTeamID(teamID),
		Email:  ac.Email,
		Name:   user.Name,
	})
}

// --- helpers ---

func generateActivationToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func hashActivationToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
