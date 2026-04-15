package cpserver

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/internal/api"
	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/oauth"
	"git.omukk.dev/wrenn/wrenn/pkg/channels"
	"git.omukk.dev/wrenn/wrenn/pkg/config"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/scheduler"
)

// Run initializes and starts the control plane server. It blocks until a
// SIGINT or SIGTERM signal is received, then shuts down gracefully.
//
// Extensions registered via WithExtensions get to add routes and start
// background workers after the core server is fully initialized.
func Run(opts ...Option) {
	o := &options{
		version: "dev",
		commit:  "unknown",
	}
	for _, opt := range opts {
		opt(o)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	cfg := config.Load()

	if len(cfg.JWTSecret) < 32 {
		slog.Error("JWT_SECRET must be at least 32 characters")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Database connection pool.
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		slog.Error("failed to ping database", "error", err)
		os.Exit(1)
	}
	slog.Info("connected to database")

	queries := db.New(pool)

	// Redis client.
	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		slog.Error("failed to parse REDIS_URL", "error", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Error("failed to ping redis", "error", err)
		os.Exit(1)
	}
	slog.Info("connected to redis")

	// mTLS is mandatory — parse internal CA for CP↔agent communication.
	if cfg.CACert == "" || cfg.CAKey == "" {
		slog.Error("WRENN_CA_CERT and WRENN_CA_KEY are required — mTLS is mandatory for CP↔agent communication")
		os.Exit(1)
	}
	ca, err := auth.ParseCA(cfg.CACert, cfg.CAKey)
	if err != nil {
		slog.Error("failed to parse mTLS CA from environment", "error", err)
		os.Exit(1)
	}
	slog.Info("mTLS enabled: CA loaded")

	// Host client pool — manages Connect RPC clients to host agents.
	cpCertStore, err := auth.NewCPCertStore(ca)
	if err != nil {
		slog.Error("failed to issue CP client certificate", "error", err)
		os.Exit(1)
	}
	// Renew the CP client certificate periodically so it never expires
	// while the control plane is running (TTL = 24h, renewal = every 12h).
	go func() {
		ticker := time.NewTicker(auth.CPCertRenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := cpCertStore.Refresh(); err != nil {
					slog.Error("failed to renew CP client certificate", "error", err)
				} else {
					slog.Info("CP client certificate renewed")
				}
			}
		}
	}()
	hostPool := lifecycle.NewHostClientPoolTLS(auth.CPClientTLSConfig(ca, cpCertStore))
	slog.Info("host client pool: mTLS enabled")

	// Scheduler — picks a host for each new sandbox (least-loaded, bottleneck-first).
	hostScheduler := scheduler.NewLeastLoadedScheduler(queries)

	// OAuth provider registry.
	oauthRegistry := oauth.NewRegistry()
	if cfg.OAuthGitHubClientID != "" && cfg.OAuthGitHubClientSecret != "" {
		if cfg.CPPublicURL == "" {
			slog.Error("CP_PUBLIC_URL must be set when OAuth providers are configured")
			os.Exit(1)
		}
		callbackURL := strings.TrimRight(cfg.CPPublicURL, "/") + "/auth/oauth/github/callback"
		ghProvider := oauth.NewGitHubProvider(cfg.OAuthGitHubClientID, cfg.OAuthGitHubClientSecret, callbackURL)
		oauthRegistry.Register(ghProvider)
		slog.Info("registered OAuth provider", "provider", "github")
	}

	// Channels: publisher, service, dispatcher.
	if len(cfg.EncryptionKeyHex) != 64 {
		slog.Error("WRENN_ENCRYPTION_KEY must be a hex-encoded 32-byte key (64 hex chars)")
		os.Exit(1)
	}
	channelPub := channels.NewPublisher(rdb)
	channelSvc := &channels.Service{DB: queries, EncKey: cfg.EncryptionKey}
	channelDispatcher := channels.NewDispatcher(rdb, queries, cfg.EncryptionKey)

	// Shared audit logger with event publishing.
	al := audit.NewWithPublisher(queries, channelPub)

	// Build the server context that extensions receive.
	sctx := ServerContext{
		Queries:   queries,
		PgPool:    pool,
		Redis:     rdb,
		HostPool:  hostPool,
		Scheduler: hostScheduler,
		CA:        ca,
		Audit:     al,
		JWTSecret: []byte(cfg.JWTSecret),
		Config:    cfg,
	}

	// API server.
	srv := api.New(queries, hostPool, hostScheduler, pool, rdb, []byte(cfg.JWTSecret), oauthRegistry, cfg.OAuthRedirectURL, ca, al, channelSvc, o.extensions, sctx)

	// Start template build workers (2 concurrent).
	stopBuildWorkers := srv.BuildSvc.StartWorkers(ctx, 2)
	defer stopBuildWorkers()

	// Start channel event dispatcher.
	channelDispatcher.Start(ctx)

	// Start host monitor (passive + active reconciliation every 30s).
	monitor := api.NewHostMonitor(queries, hostPool, al, 30*time.Second)
	monitor.Start(ctx)

	// Start metrics sampler (records per-team sandbox stats every 10s).
	sampler := api.NewMetricsSampler(queries, 10*time.Second)
	sampler.Start(ctx)

	// Start extension background workers.
	for _, ext := range o.extensions {
		for _, worker := range ext.BackgroundWorkers(sctx) {
			worker(ctx)
		}
	}

	// Wrap the API handler with the sandbox proxy so that requests with
	// {port}-{sandbox_id}.{domain} Host headers are routed to the sandbox's
	// host agent. All other requests pass through to the normal API router.
	proxyWrapper := api.NewSandboxProxyWrapper(srv.Handler(), queries, hostPool)

	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: proxyWrapper,
	}

	// Graceful shutdown on signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("http server shutdown error", "error", err)
		}
	}()

	slog.Info("control plane starting", "addr", cfg.ListenAddr, "version", o.version, "commit", o.commit)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("http server error", "error", err)
		os.Exit(1)
	}

	slog.Info("control plane stopped")
}
