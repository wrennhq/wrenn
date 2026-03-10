package config

import (
	"os"
	"strings"
)

// Config holds the control plane configuration.
type Config struct {
	DatabaseURL   string
	ListenAddr    string
	HostAgentAddr string
}

// Load reads configuration from environment variables.
func Load() Config {
	cfg := Config{
		DatabaseURL:   envOrDefault("DATABASE_URL", "postgres://wrenn:wrenn@localhost:5432/wrenn?sslmode=disable"),
		ListenAddr:    envOrDefault("CP_LISTEN_ADDR", ":8080"),
		HostAgentAddr: envOrDefault("CP_HOST_AGENT_ADDR", "http://localhost:50051"),
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
