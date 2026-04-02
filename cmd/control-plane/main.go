package main

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

	"git.omukk.dev/wrenn/sandbox/internal/api"
	"git.omukk.dev/wrenn/sandbox/internal/audit"
	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/auth/oauth"
	"git.omukk.dev/wrenn/sandbox/internal/config"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	"git.omukk.dev/wrenn/sandbox/internal/scheduler"
)

func main() {
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

	// mTLS: parse internal CA and build a TLS-capable host client pool.
	// When CA env vars are absent the pool falls back to plain HTTP (dev mode).
	var ca *auth.CA
	if cfg.CACert != "" && cfg.CAKey != "" {
		var err error
		ca, err = auth.ParseCA(cfg.CACert, cfg.CAKey)
		if err != nil {
			slog.Error("failed to parse mTLS CA from environment", "error", err)
			os.Exit(1)
		}
		slog.Info("mTLS enabled: CA loaded")
	} else {
		slog.Warn("mTLS disabled: WRENN_CA_CERT/WRENN_CA_KEY not set — host agent connections are unencrypted")
	}

	// Host client pool — manages Connect RPC clients to host agents.
	var hostPool *lifecycle.HostClientPool
	if ca != nil {
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
		hostPool = lifecycle.NewHostClientPoolTLS(auth.CPClientTLSConfig(ca, cpCertStore))
		slog.Info("host client pool: mTLS enabled")
	} else {
		hostPool = lifecycle.NewHostClientPool()
	}

	// Scheduler — picks a host for each new sandbox (round-robin for now).
	hostScheduler := scheduler.NewRoundRobinScheduler(queries)

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

	// API server.
	srv := api.New(queries, hostPool, hostScheduler, pool, rdb, []byte(cfg.JWTSecret), oauthRegistry, cfg.OAuthRedirectURL, ca)

	// Start template build workers (2 concurrent).
	stopBuildWorkers := srv.BuildSvc.StartWorkers(ctx, 2)
	defer stopBuildWorkers()

	// Start host monitor (passive + active reconciliation every 30s).
	monitor := api.NewHostMonitor(queries, hostPool, audit.New(queries), 30*time.Second)
	monitor.Start(ctx)

	// Start metrics sampler (records per-team sandbox stats every 10s).
	sampler := api.NewMetricsSampler(queries, 10*time.Second)
	sampler.Start(ctx)

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

	slog.Info("control plane starting", "addr", cfg.ListenAddr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("http server error", "error", err)
		os.Exit(1)
	}

	slog.Info("control plane stopped")
}
