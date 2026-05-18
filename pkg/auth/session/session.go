// Package session implements opaque cookie-backed user sessions for the
// browser-facing control plane. Sessions are stored durably in Postgres
// (sessions table) and cached in Redis (wrenn:session:{sid}) for the hot
// auth-middleware path.
//
// SIDs are 32 random bytes hex-encoded. CSRF tokens are issued alongside
// each session and rotated on session rotation (e.g. team switch).
//
// Expiry has two limits:
//   - Idle: IdleWindow (6h) — Redis TTL slides on each successful Get.
//   - Absolute: AbsoluteCap (24h) — stored as expires_at; never extended.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/pkg/db"
)

const (
	// IdleWindow caps how long a session can sit idle before it expires.
	IdleWindow = 6 * time.Hour
	// AbsoluteCap caps total session lifetime regardless of activity.
	AbsoluteCap = 24 * time.Hour
	// touchDBInterval is the minimum gap between Postgres last_seen_at updates
	// for the same session. Redis TTL is bumped on every request; the DB is
	// only updated when stale by more than this interval.
	touchDBInterval = 1 * time.Minute

	redisKeyPrefix = "wrenn:session:"
)

// ErrNotFound is returned when no session exists for the given SID.
var ErrNotFound = errors.New("session: not found")

// ErrExpired is returned when a session is past its absolute cap.
var ErrExpired = errors.New("session: expired")

// Session is the in-memory representation of a logged-in user. The fields
// after the identity block are denormalized from the users + team_members
// tables for fast middleware lookups; they are refreshed on rotation and
// invalidated by Revoke/RevokeAllForUser on identity changes.
type Session struct {
	ID         string      `json:"id"`
	UserID     pgtype.UUID `json:"user_id"`
	TeamID     pgtype.UUID `json:"team_id"`
	CSRFToken  string      `json:"csrf"`
	Email      string      `json:"email"`
	Name       string      `json:"name"`
	Role       string      `json:"role"`
	IsAdmin    bool        `json:"is_admin"`
	CreatedAt  time.Time   `json:"created_at"`
	ExpiresAt  time.Time   `json:"expires_at"`
	LastSeenAt time.Time   `json:"last_seen_at"`
	UserAgent  string      `json:"user_agent"`
	IPAddress  string      `json:"ip"`
}

// Service issues, validates, and revokes sessions.
type Service struct {
	db  *db.Queries
	rdb *redis.Client
}

// NewService constructs a session service backed by the given queries and
// Redis client.
func NewService(q *db.Queries, rdb *redis.Client) *Service {
	return &Service{db: q, rdb: rdb}
}

// GenerateSID returns a fresh 32-byte hex-encoded session identifier.
func GenerateSID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateCSRFToken returns a fresh 32-byte hex-encoded CSRF token.
func GenerateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Create issues a new session for the given user. Email, name, role and
// is_admin are stamped into the session blob and used by middleware without
// further DB lookups (except for admin gates, which always re-check the DB).
func (s *Service) Create(
	ctx context.Context,
	userID, teamID pgtype.UUID,
	email, name, role string,
	isAdmin bool,
	userAgent, ipAddress string,
) (*Session, error) {
	sid, err := GenerateSID()
	if err != nil {
		return nil, fmt.Errorf("generate sid: %w", err)
	}
	csrf, err := GenerateCSRFToken()
	if err != nil {
		return nil, fmt.Errorf("generate csrf: %w", err)
	}

	now := time.Now().UTC()
	expiresAt := now.Add(AbsoluteCap)

	row, err := s.db.InsertSession(ctx, db.InsertSessionParams{
		ID:        sid,
		UserID:    userID,
		TeamID:    teamID,
		CsrfToken: csrf,
		UserAgent: userAgent,
		IpAddress: ipAddress,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}

	sess := &Session{
		ID:         row.ID,
		UserID:     row.UserID,
		TeamID:     row.TeamID,
		CSRFToken:  row.CsrfToken,
		Email:      email,
		Name:       name,
		Role:       role,
		IsAdmin:    isAdmin,
		CreatedAt:  row.CreatedAt.Time,
		ExpiresAt:  row.ExpiresAt.Time,
		LastSeenAt: row.LastSeenAt.Time,
		UserAgent:  row.UserAgent,
		IPAddress:  row.IpAddress,
	}

	if err := s.writeCache(ctx, sess); err != nil {
		// Cache failures are non-fatal — middleware will fall back to DB.
		slog.Warn("session: write cache failed", "error", err)
	}
	return sess, nil
}

// Get loads a session by SID, validates expiry, and slides the idle window.
// It returns ErrNotFound if the session does not exist (or has been revoked)
// and ErrExpired if it is past its absolute cap.
//
// The hydrate callback is invoked on cache miss to refetch identity columns
// (email, name, role, is_admin) from the source tables before the session is
// repopulated into Redis. Pass nil to skip identity refresh.
func (s *Service) Get(
	ctx context.Context,
	sid string,
	hydrate func(context.Context, *Session) error,
) (*Session, error) {
	if sid == "" {
		return nil, ErrNotFound
	}

	now := time.Now().UTC()

	// Cache hit fast path.
	if sess, ok, err := s.readCache(ctx, sid); err != nil {
		slog.Warn("session: read cache failed", "error", err)
	} else if ok {
		if now.After(sess.ExpiresAt) {
			_ = s.Revoke(ctx, sid)
			return nil, ErrExpired
		}
		s.slideIdle(ctx, sess)
		s.maybeTouchDB(ctx, sess, now)
		return sess, nil
	}

	// Cache miss — fall back to DB.
	row, err := s.db.GetSession(ctx, sid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get session: %w", err)
	}
	if now.After(row.ExpiresAt.Time) {
		_ = s.db.DeleteSession(ctx, sid)
		return nil, ErrExpired
	}

	sess := &Session{
		ID:         row.ID,
		UserID:     row.UserID,
		TeamID:     row.TeamID,
		CSRFToken:  row.CsrfToken,
		CreatedAt:  row.CreatedAt.Time,
		ExpiresAt:  row.ExpiresAt.Time,
		LastSeenAt: row.LastSeenAt.Time,
		UserAgent:  row.UserAgent,
		IPAddress:  row.IpAddress,
	}
	if hydrate != nil {
		if err := hydrate(ctx, sess); err != nil {
			return nil, fmt.Errorf("hydrate session: %w", err)
		}
	}
	if err := s.writeCache(ctx, sess); err != nil {
		slog.Warn("session: write cache failed", "error", err)
	}
	s.maybeTouchDB(ctx, sess, now)
	return sess, nil
}

// Rotate revokes oldSID and issues a fresh session with possibly updated
// team/role. Used on team switch and any privilege change.
func (s *Service) Rotate(
	ctx context.Context,
	oldSID string,
	userID, teamID pgtype.UUID,
	email, name, role string,
	isAdmin bool,
	userAgent, ipAddress string,
) (*Session, error) {
	if err := s.Revoke(ctx, oldSID); err != nil {
		return nil, fmt.Errorf("revoke old: %w", err)
	}
	return s.Create(ctx, userID, teamID, email, name, role, isAdmin, userAgent, ipAddress)
}

// Revoke deletes a single session by SID from both Redis and Postgres.
func (s *Service) Revoke(ctx context.Context, sid string) error {
	if sid == "" {
		return nil
	}
	if err := s.rdb.Del(ctx, redisKey(sid)).Err(); err != nil {
		slog.Warn("session: del cache failed", "error", err)
	}
	if err := s.db.DeleteSession(ctx, sid); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// RevokeAllForUser deletes every session for a user. Used on password
// add/change/reset and on logout-all.
func (s *Service) RevokeAllForUser(ctx context.Context, userID pgtype.UUID) error {
	ids, err := s.db.DeleteSessionsByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	pipe := s.rdb.Pipeline()
	for _, id := range ids {
		pipe.Del(ctx, redisKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("session: pipeline del failed", "error", err)
	}
	return nil
}

// ListForUser returns all active session rows for a user, newest activity
// first. Backed by Postgres directly — the Redis cache is opportunistic and
// is not consulted here.
func (s *Service) ListForUser(ctx context.Context, userID pgtype.UUID) ([]db.Session, error) {
	rows, err := s.db.ListSessionsByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return rows, nil
}

// DeleteForUser deletes a single session if it belongs to the given user.
// Returns no error if the SID does not exist or belongs to someone else
// (caller is treated as having already lost interest in it).
func (s *Service) DeleteForUser(ctx context.Context, sid string, userID pgtype.UUID) error {
	if err := s.rdb.Del(ctx, redisKey(sid)).Err(); err != nil {
		slog.Warn("session: del cache failed", "error", err)
	}
	if err := s.db.DeleteSessionForUser(ctx, db.DeleteSessionForUserParams{ID: sid, UserID: userID}); err != nil {
		return fmt.Errorf("delete session for user: %w", err)
	}
	return nil
}

// InvalidateCacheForUser drops Redis cache entries for every session
// belonging to the given user without revoking the underlying DB rows.
// Next request rehydrates the session from Postgres + identity tables —
// useful after a name change so cached identity is refreshed cheaply.
func (s *Service) InvalidateCacheForUser(ctx context.Context, userID pgtype.UUID) error {
	rows, err := s.db.ListSessionsByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	pipe := s.rdb.Pipeline()
	for _, row := range rows {
		pipe.Del(ctx, redisKey(row.ID))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("invalidate cache: %w", err)
	}
	return nil
}

// UpdateTeam mutates the team_id on the current session in place (for the
// non-rotation switch-team path; we do rotate in handlers, but the helper
// is kept for completeness).
func (s *Service) UpdateTeam(ctx context.Context, sid string, teamID pgtype.UUID) error {
	if err := s.db.UpdateSessionTeam(ctx, db.UpdateSessionTeamParams{ID: sid, TeamID: teamID}); err != nil {
		return fmt.Errorf("update session team: %w", err)
	}
	_ = s.rdb.Del(ctx, redisKey(sid))
	return nil
}

// StartCleaner returns a background-worker function that periodically prunes
// rows whose absolute expiry has passed. Register it via
// cpserver/cpextension BackgroundWorkers wiring.
func (s *Service) StartCleaner() func(context.Context) {
	return func(ctx context.Context) {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.db.DeleteExpiredSessions(ctx); err != nil {
					slog.Warn("session: delete expired failed", "error", err)
				}
			}
		}
	}
}

// --- internals ---

func redisKey(sid string) string { return redisKeyPrefix + sid }

func (s *Service) readCache(ctx context.Context, sid string) (*Session, bool, error) {
	raw, err := s.rdb.Get(ctx, redisKey(sid)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, false, err
	}
	return &sess, true, nil
}

func (s *Service) writeCache(ctx context.Context, sess *Session) error {
	buf, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	ttl := time.Until(sess.ExpiresAt)
	if ttl > IdleWindow {
		ttl = IdleWindow
	}
	if ttl <= 0 {
		return nil
	}
	return s.rdb.Set(ctx, redisKey(sess.ID), buf, ttl).Err()
}

func (s *Service) slideIdle(ctx context.Context, sess *Session) {
	ttl := time.Until(sess.ExpiresAt)
	if ttl > IdleWindow {
		ttl = IdleWindow
	}
	if ttl <= 0 {
		return
	}
	if err := s.rdb.Expire(ctx, redisKey(sess.ID), ttl).Err(); err != nil {
		slog.Warn("session: expire failed", "error", err)
	}
}

func (s *Service) maybeTouchDB(ctx context.Context, sess *Session, now time.Time) {
	if now.Sub(sess.LastSeenAt) < touchDBInterval {
		return
	}
	sid := sess.ID
	sess.LastSeenAt = now
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.db.TouchSession(c, sid); err != nil {
			slog.Warn("session: touch db failed", "error", err)
		}
	}()
}
