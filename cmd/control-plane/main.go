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
	"git.omukk.dev/wrenn/sandbox/internal/auth/oauth"
	"git.omukk.dev/wrenn/sandbox/internal/config"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
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

	// Connect RPC client for the host agent.
	agentHTTP := &http.Client{Timeout: 10 * time.Minute}
	agentClient := hostagentv1connect.NewHostAgentServiceClient(
		agentHTTP,
		cfg.HostAgentAddr,
	)

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
	srv := api.New(queries, agentClient, pool, rdb, []byte(cfg.JWTSecret), oauthRegistry, cfg.OAuthRedirectURL)

	// Start reconciler.
	reconciler := api.NewReconciler(queries, agentClient, "default", 5*time.Second)
	reconciler.Start(ctx)

	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: srv.Handler(),
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

	slog.Info("control plane starting", "addr", cfg.ListenAddr, "agent", cfg.HostAgentAddr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("http server error", "error", err)
		os.Exit(1)
	}

	slog.Info("control plane stopped")
}
