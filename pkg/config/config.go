package config

import (
	"encoding/hex"
	"os"
	"strconv"

	"github.com/joho/godotenv"

	"git.omukk.dev/wrenn/wrenn/internal/units"
)

// Config holds the control plane configuration.
type Config struct {
	DatabaseURL string
	RedisURL    string
	ListenAddr  string
	JWTSecret   string
	WrennDir    string // WRENN_DIR — base directory for wrenn data (logs, etc.)

	// mTLS — CP→Agent channel. Both must be set to enable mTLS; omitting either
	// disables cert issuance and leaves agent connections on plain HTTP (dev mode).
	CACert string // WRENN_CA_CERT — PEM-encoded internal CA certificate
	CAKey  string // WRENN_CA_KEY  — PEM-encoded internal CA private key

	OAuthGitHubClientID     string
	OAuthGitHubClientSecret string
	OAuthRedirectURL        string
	CPPublicURL             string

	// Channels — encryption for channel secrets (AES-256-GCM).
	EncryptionKeyHex string   // WRENN_ENCRYPTION_KEY raw hex string (for validation)
	EncryptionKey    [32]byte // parsed 32-byte key

	// ChannelsAllowPrivateTargets permits notification channels (webhooks etc.)
	// to point at private/loopback/link-local addresses. Off by default to
	// prevent SSRF against internal services; enable only on self-hosted
	// deployments that legitimately deliver to internal endpoints.
	ChannelsAllowPrivateTargets bool // WRENN_CHANNELS_ALLOW_PRIVATE

	// MaxVolumeSizeMB caps how large a single external storage volume may be.
	// WRENN_MAX_VOLUME_SIZE accepts the same human-readable form as
	// WRENN_DEFAULT_ROOTFS_SIZE (e.g. "20Gi", "500G", "2048M"). Raise it on
	// self-hosted deployments with the disk to back it — a volume is a sparse
	// file, so the cap bounds worst-case growth rather than upfront allocation.
	// 0 means unset; service.DefaultMaxVolumeSizeMB (20Gi) then applies.
	MaxVolumeSizeMB int // WRENN_MAX_VOLUME_SIZE

	// SMTP — transactional email. All fields optional; omitting SMTPHost disables email.
	SMTPHost      string // SMTP_HOST
	SMTPPort      int    // SMTP_PORT (default 587)
	SMTPUsername  string // SMTP_USERNAME
	SMTPPassword  string // SMTP_PASSWORD
	SMTPFromEmail string // SMTP_FROM_EMAIL
}

// Load reads configuration from a .env file (if present) and environment variables.
// Real environment variables take precedence over .env values.
func Load() Config {
	// Best-effort load — missing .env file is fine.
	_ = godotenv.Load()

	cfg := Config{
		DatabaseURL: envOrDefault("DATABASE_URL", "postgres://wrenn:wrenn@localhost:5432/wrenn?sslmode=disable"),
		RedisURL:    envOrDefault("REDIS_URL", "redis://localhost:6379/0"),
		ListenAddr:  envOrDefault("WRENN_CP_LISTEN_ADDR", ":9725"),
		JWTSecret:   os.Getenv("JWT_SECRET"),
		WrennDir:    envOrDefault("WRENN_DIR", "/var/lib/wrenn"),

		CACert: os.Getenv("WRENN_CA_CERT"),
		CAKey:  os.Getenv("WRENN_CA_KEY"),

		OAuthGitHubClientID:     os.Getenv("OAUTH_GITHUB_CLIENT_ID"),
		OAuthGitHubClientSecret: os.Getenv("OAUTH_GITHUB_CLIENT_SECRET"),
		OAuthRedirectURL:        envOrDefault("OAUTH_REDIRECT_URL", "https://app.wrenn.dev"),
		CPPublicURL:             os.Getenv("CP_PUBLIC_URL"),

		EncryptionKeyHex: os.Getenv("WRENN_ENCRYPTION_KEY"),

		ChannelsAllowPrivateTargets: envOrDefaultBool("WRENN_CHANNELS_ALLOW_PRIVATE", false),

		// 0 means "unset" — service.VolumeService owns the default so the
		// limit is not declared in two places that can drift apart.
		MaxVolumeSizeMB: envOrDefaultSizeMB("WRENN_MAX_VOLUME_SIZE", 0),

		SMTPHost:      os.Getenv("SMTP_HOST"),
		SMTPPort:      envOrDefaultInt("SMTP_PORT", 587),
		SMTPUsername:  os.Getenv("SMTP_USERNAME"),
		SMTPPassword:  os.Getenv("SMTP_PASSWORD"),
		SMTPFromEmail: envOrDefault("SMTP_FROM_EMAIL", "noreply@wrenn.dev"),
	}

	if cfg.EncryptionKeyHex != "" {
		b, err := hex.DecodeString(cfg.EncryptionKeyHex)
		if err == nil && len(b) == 32 {
			copy(cfg.EncryptionKey[:], b)
		}
	}

	return cfg
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envOrDefaultInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// envOrDefaultSizeMB reads a human-readable size (e.g. "20Gi", "500G", "2048M")
// and returns it in megabytes, falling back to def when unset or unparseable —
// matching the fail-soft behaviour of the other envOrDefault helpers.
func envOrDefaultSizeMB(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	mb, err := units.ParseSizeToMB(v)
	if err != nil || mb <= 0 {
		return def
	}
	return mb
}

func envOrDefaultBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
