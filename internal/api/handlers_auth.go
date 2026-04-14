package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.omukk.dev/wrenn/wrenn/internal/auth"
	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/id"
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

type switchTeamRequest struct {
	TeamID string `json:"team_id"`
}

type authHandler struct {
	db        *db.Queries
	pool      *pgxpool.Pool
	jwtSecret []byte
}

func newAuthHandler(db *db.Queries, pool *pgxpool.Pool, jwtSecret []byte) *authHandler {
	return &authHandler{db: db, pool: pool, jwtSecret: jwtSecret}
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

type authResponse struct {
	Token  string `json:"token"`
	UserID string `json:"user_id"`
	TeamID string `json:"team_id"`
	Email  string `json:"email"`
	Name   string `json:"name"`
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

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to hash password")
		return
	}

	// Use a transaction to atomically create user + team + membership.
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to begin transaction")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	qtx := h.db.WithTx(tx)

	// The first user to sign up becomes a platform admin.
	userCount, err := qtx.CountUsers(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to check user count")
		return
	}
	isFirstUser := userCount == 0

	userID := id.NewUserID()
	_, err = qtx.InsertUser(ctx, db.InsertUserParams{
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

	if isFirstUser {
		if err := qtx.SetUserAdmin(ctx, db.SetUserAdminParams{ID: userID, IsAdmin: true}); err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "failed to set admin status")
			return
		}
	}

	// Create default team.
	teamID := id.NewTeamID()
	if _, err := qtx.InsertTeam(ctx, db.InsertTeamParams{
		ID:   teamID,
		Name: req.Name + "'s Team",
		Slug: id.NewTeamSlug(),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to create team")
		return
	}

	if err := qtx.InsertTeamMember(ctx, db.InsertTeamMemberParams{
		UserID:    userID,
		TeamID:    teamID,
		IsDefault: true,
		Role:      "owner",
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to add user to team")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to commit signup")
		return
	}

	token, err := auth.SignJWT(h.jwtSecret, userID, teamID, req.Email, req.Name, "owner", isFirstUser)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate token")
		return
	}

	writeJSON(w, http.StatusCreated, authResponse{
		Token:  token,
		UserID: id.FormatUserID(userID),
		TeamID: id.FormatTeamID(teamID),
		Email:  req.Email,
		Name:   req.Name,
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

	if !user.IsActive {
		slog.Warn("login failed: account deactivated", "email", req.Email, "ip", r.RemoteAddr)
		writeError(w, http.StatusForbidden, "account_deactivated", "your account has been deactivated — contact your administrator to regain access")
		return
	}

	team, role, err := loginTeam(ctx, h.db, user.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusForbidden, "no_team", "user is not a member of any team")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "failed to look up team")
		return
	}

	token, err := auth.SignJWT(h.jwtSecret, user.ID, team.ID, user.Email, user.Name, role, user.IsAdmin)
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
