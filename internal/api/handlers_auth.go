package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
)

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
}

// Signup handles POST /v1/auth/signup.
func (h *authHandler) Signup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if !strings.Contains(req.Email, "@") || len(req.Email) < 3 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid email address")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "invalid_request", "password must be at least 8 characters")
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

	userID := id.NewUserID()
	_, err = qtx.InsertUser(ctx, db.InsertUserParams{
		ID:           userID,
		Email:        req.Email,
		PasswordHash: pgtype.Text{String: passwordHash, Valid: true},
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

	// Create default team.
	teamID := id.NewTeamID()
	if _, err := qtx.InsertTeam(ctx, db.InsertTeamParams{
		ID:   teamID,
		Name: req.Email + "'s Team",
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

	token, err := auth.SignJWT(h.jwtSecret, userID, teamID, req.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate token")
		return
	}

	writeJSON(w, http.StatusCreated, authResponse{
		Token:  token,
		UserID: userID,
		TeamID: teamID,
		Email:  req.Email,
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
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid email or password")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "failed to look up user")
		return
	}

	if !user.PasswordHash.Valid {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid email or password")
		return
	}
	if err := auth.CheckPassword(user.PasswordHash.String, req.Password); err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "invalid email or password")
		return
	}

	team, err := h.db.GetDefaultTeamForUser(ctx, user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to look up team")
		return
	}

	token, err := auth.SignJWT(h.jwtSecret, user.ID, team.ID, user.Email)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to generate token")
		return
	}

	writeJSON(w, http.StatusOK, authResponse{
		Token:  token,
		UserID: user.ID,
		TeamID: team.ID,
		Email:  user.Email,
	})
}
