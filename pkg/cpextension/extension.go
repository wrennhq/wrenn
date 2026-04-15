// Package cpextension defines the types for extending the control plane server.
// This package is intentionally minimal and dependency-free (relative to internal/)
// to avoid import cycles between pkg/cpserver and internal/api.
package cpextension

import (
	"context"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/config"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/scheduler"
)

// ServerContext exposes the initialized dependencies that extensions can use
// to register routes and start background workers. All fields are read-only
// from the extension's perspective.
type ServerContext struct {
	Queries   *db.Queries
	PgPool    *pgxpool.Pool
	Redis     *redis.Client
	HostPool  *lifecycle.HostClientPool
	Scheduler scheduler.HostScheduler
	CA        *auth.CA
	Audit     *audit.AuditLogger
	JWTSecret []byte
	Config    config.Config
}

// Extension allows enterprise (or any external) code to plug additional
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
