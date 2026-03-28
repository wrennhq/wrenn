package config

import (
	"os"

	"github.com/joho/godotenv"
)

// Config holds the control plane configuration.
type Config struct {
	DatabaseURL string
	RedisURL    string
	ListenAddr  string
	JWTSecret   string

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

	return Config{
		DatabaseURL: envOrDefault("DATABASE_URL", "postgres://wrenn:wrenn@localhost:5432/wrenn?sslmode=disable"),
		RedisURL:    envOrDefault("REDIS_URL", "redis://localhost:6379/0"),
		ListenAddr:  envOrDefault("WRENN_CP_LISTEN_ADDR", ":8080"),
		JWTSecret:   os.Getenv("JWT_SECRET"),

		OAuthGitHubClientID:     os.Getenv("OAUTH_GITHUB_CLIENT_ID"),
		OAuthGitHubClientSecret: os.Getenv("OAUTH_GITHUB_CLIENT_SECRET"),
		OAuthRedirectURL:        envOrDefault("OAUTH_REDIRECT_URL", "https://app.wrenn.dev"),
		CPPublicURL:             os.Getenv("CP_PUBLIC_URL"),
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
