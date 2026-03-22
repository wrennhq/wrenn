package config

import (
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds the control plane configuration.
type Config struct {
	DatabaseURL   string
	RedisURL      string
	ListenAddr    string
	HostAgentAddr string
	JWTSecret     string

	OAuthGitHubClientID     string
	OAuthGitHubClientSecret string
	OAuthRedirectURL        string
	CPPublicURL             string
}

// Load reads configuration from a .env file (if present) and environment variables.
// Real environment variables take precedence over .env values.
func Load() Config {
	// Best-effort load — missing .env file is fine.
	_ = godotenv.Load()

	cfg := Config{
		DatabaseURL:   envOrDefault("DATABASE_URL", "postgres://wrenn:wrenn@localhost:5432/wrenn?sslmode=disable"),
		RedisURL:      envOrDefault("REDIS_URL", "redis://localhost:6379/0"),
		ListenAddr:    envOrDefault("CP_LISTEN_ADDR", ":8080"),
		HostAgentAddr: envOrDefault("CP_HOST_AGENT_ADDR", "http://localhost:50051"),
		JWTSecret:     os.Getenv("JWT_SECRET"),

		OAuthGitHubClientID:     os.Getenv("OAUTH_GITHUB_CLIENT_ID"),
		OAuthGitHubClientSecret: os.Getenv("OAUTH_GITHUB_CLIENT_SECRET"),
		OAuthRedirectURL:        envOrDefault("OAUTH_REDIRECT_URL", "https://app.wrenn.dev"),
		CPPublicURL:             os.Getenv("CP_PUBLIC_URL"),
	}

	// Ensure the host agent address has a scheme.
	if !strings.HasPrefix(cfg.HostAgentAddr, "http://") && !strings.HasPrefix(cfg.HostAgentAddr, "https://") {
		cfg.HostAgentAddr = "http://" + cfg.HostAgentAddr
	}

	return cfg
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
