// Package cpextension defines the types for extending the control plane server.
// This package is intentionally minimal and dependency-free (relative to internal/)
// to avoid import cycles between pkg/cpserver and internal/api.
package cpextension

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/oauth"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/session"
	sessionmw "git.omukk.dev/wrenn/wrenn/pkg/auth/session/middleware"
	"git.omukk.dev/wrenn/wrenn/pkg/channels"
	"git.omukk.dev/wrenn/wrenn/pkg/config"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/email"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/scheduler"
)

// Re-exported cookie / header names so extensions can reference the canonical
// values without depending on the middleware sub-package directly.
const (
	SessionCookieName = sessionmw.SessionCookieName
	CSRFCookieName    = sessionmw.CSRFCookieName
	CSRFHeaderName    = sessionmw.CSRFHeaderName
)

// ServerContext exposes the initialized dependencies that extensions can use
// to register routes and start background workers. All fields are read-only
// from the extension's perspective.
type ServerContext struct {
	Queries       *db.Queries
	PgPool        *pgxpool.Pool
	Redis         *redis.Client
	HostPool      *lifecycle.HostClientPool
	Scheduler     scheduler.HostScheduler
	CA            *auth.CA
	Audit         *audit.AuditLogger
	Mailer        email.Mailer
	OAuthRegistry *oauth.Registry
	Channels      *channels.Service
	ChannelPub    *channels.Publisher
	// JWTSecret signs host-agent tokens and HMACs OAuth state cookies. User
	// auth uses cookie-backed sessions and does not depend on this value —
	// extensions should not use it to verify user identity.
	JWTSecret []byte
	Sessions  *session.Service
	Config    config.Config
}

// Extension allows cloud (or any external) code to plug additional
// routes and background workers into the control plane without modifying
// the core server.
type Extension interface {
	// RegisterRoutes is called after all core routes are registered.
	// The chi.Router supports sub-routing, middleware, etc.
	RegisterRoutes(r chi.Router, ctx ServerContext)

	// BackgroundWorkers returns functions that will be called once with
	// the application context after the server is fully initialized.
	// Each function should start its own goroutine(s) and return.
	BackgroundWorkers(ctx ServerContext) []func(context.Context)
}

// MiddlewareProvider is optionally implemented by extensions that need
// middleware applied before OSS routes are registered. This allows
// cloud middleware to wrap existing OSS routes (e.g. billing checks).
type MiddlewareProvider interface {
	Middlewares(ctx ServerContext) []func(http.Handler) http.Handler
}

// AuthHook is optionally implemented by extensions that need to react to
// identity lifecycle events. OnSignup runs synchronously inside the signup
// handler — returning an error fails the request, which is the contract
// billing extensions rely on (no Wrenn user without a Lago customer).
// OnLogin and the delete hooks are fire-and-forget at the call site: errors
// are logged but never block the user-visible flow.
type AuthHook interface {
	OnSignup(ctx context.Context, userID, teamID pgtype.UUID, email string) error
	OnLogin(ctx context.Context, userID pgtype.UUID) error
	OnAccountSoftDelete(ctx context.Context, userID pgtype.UUID) error
	OnAccountHardDelete(ctx context.Context, userID pgtype.UUID) error
}

// SandboxEvent is the canonical payload handed to SandboxEventHook
// implementations. The Type field uses the public verb names ("created",
// "started", "paused", "resumed", "stopped", "destroyed").
type SandboxEvent struct {
	SandboxID  pgtype.UUID
	TeamID     pgtype.UUID
	Type       string
	OccurredAt time.Time
	Metadata   map[string]any
}

// SandboxEventHook is optionally implemented by extensions that need to react
// to sandbox lifecycle events for metering, audit shipping, etc. The hook is
// invoked from inside the Redis stream consumer; returning an error causes
// the message to be left un-acked so it will be redelivered. Hooks must be
// idempotent.
type SandboxEventHook interface {
	OnSandboxEvent(ctx context.Context, ev SandboxEvent) error
}

// Limits describes the per-team quota envelope.
type Limits struct {
	MaxConcurrentSandboxes int
	MaxVCPUs               int
	MaxMemoryMB            int
	MaxStorageGB           int
	MaxSnapshots           int
}

// LimitsProvider is optionally implemented by an extension that owns billing
// plans. The sandbox create handler consults it before scheduling work; if no
// extension provides limits, OSS defaults apply.
type LimitsProvider interface {
	EffectiveLimits(ctx context.Context, teamID pgtype.UUID) (Limits, error)
}

// Usage describes a team's current resource footprint.
type Usage struct {
	ConcurrentSandboxes int
	VCPUsInUse          int
	MemoryMBInUse       int
	SnapshotCount       int
	SnapshotStorageGB   int
}

// UsageProvider returns the current usage for a team. Consumed by the
// LimitsProvider gate and by extension dashboards.
type UsageProvider interface {
	CurrentUsage(ctx context.Context, teamID pgtype.UUID) (Usage, error)
}

// --- Auth middleware helpers exposed to extensions ---

// RequireSession returns middleware that enforces a valid session cookie.
func RequireSession(sctx ServerContext) func(http.Handler) http.Handler {
	return sessionmw.RequireSession(sctx.Sessions, sctx.Queries)
}

// RequireSessionOrAPIKey returns middleware that accepts either an X-API-Key
// header (SDKs) or a wrenn_sid cookie (browser).
func RequireSessionOrAPIKey(sctx ServerContext) func(http.Handler) http.Handler {
	return sessionmw.RequireSessionOrAPIKey(sctx.Sessions, sctx.Queries)
}

// RequireAdmin returns middleware that gates routes on platform-admin status.
// Must run after RequireSession.
func RequireAdmin(sctx ServerContext) func(http.Handler) http.Handler {
	return sessionmw.RequireAdmin(sctx.Queries)
}

// IssueSession creates a fresh session for the given user/team and writes
// the cookies onto the response. Identity columns are looked up from the DB.
func IssueSession(w http.ResponseWriter, r *http.Request, sctx ServerContext, userID, teamID pgtype.UUID) (*session.Session, error) {
	return sessionmw.IssueSession(r.Context(), w, r, sctx.Queries, sctx.Sessions, userID, teamID)
}

// ClearSessionCookies invalidates the session and CSRF cookies. Suitable for
// extension logout flows that aren't routed through OSS handlers.
func ClearSessionCookies(w http.ResponseWriter, r *http.Request) {
	sessionmw.ClearCookies(w, sessionmw.IsSecure(r))
}
