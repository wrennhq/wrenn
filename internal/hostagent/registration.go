package hostagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// TokenFile is the JSON format persisted to WRENN_DIR/host-credentials.json.
// It holds all credentials the agent needs: the host JWT, refresh token, and
// (when mTLS is enabled) the TLS certificate material for the agent's server.
type TokenFile struct {
	HostID       string `json:"host_id"`
	JWT          string `json:"jwt"`
	RefreshToken string `json:"refresh_token"`
	// mTLS fields — empty when the CP has no CA configured.
	CertPEM   string `json:"cert_pem,omitempty"`
	KeyPEM    string `json:"key_pem,omitempty"`
	CACertPEM string `json:"ca_cert_pem,omitempty"`
}

// RegistrationConfig holds the configuration for host registration.
type RegistrationConfig struct {
	CPURL             string // Control plane base URL (e.g., http://localhost:8000)
	RegistrationToken string // One-time registration token from the control plane
	TokenFile         string // Path to persist the credentials after registration
	Address           string // Externally-reachable address (ip:port) for this host
}

type registerRequest struct {
	Token    string `json:"token"`
	Arch     string `json:"arch"`
	CPUCores int32  `json:"cpu_cores"`
	MemoryMB int32  `json:"memory_mb"`
	DiskGB   int32  `json:"disk_gb"`
	Address  string `json:"address"`
}

// authResponse is the shared JSON shape for both register and refresh responses.
type authResponse struct {
	Host         json.RawMessage `json:"host"`
	Token        string          `json:"token"`
	RefreshToken string          `json:"refresh_token"`
	CertPEM      string          `json:"cert_pem,omitempty"`
	KeyPEM       string          `json:"key_pem,omitempty"`
	CACertPEM    string          `json:"ca_cert_pem,omitempty"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// LoadTokenFile reads and parses the persisted credentials file.
func LoadTokenFile(path string) (*TokenFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Support legacy format (raw JWT string) for backwards compatibility.
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasPrefix(trimmed, "{") {
		// Old format: just the JWT, no refresh token.
		hostID, _ := hostIDFromJWT(trimmed)
		return &TokenFile{HostID: hostID, JWT: trimmed}, nil
	}
	var tf TokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("parse credentials file: %w", err)
	}
	return &tf, nil
}

// saveTokenFile writes the credentials file as JSON with 0600 permissions.
func saveTokenFile(path string, tf TokenFile) error {
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials file: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

// Register calls the control plane to register this host agent and persists
// the returned credentials to disk. Returns the full TokenFile on success.
func Register(ctx context.Context, cfg RegistrationConfig) (*TokenFile, error) {
	// If no explicit registration token was given, reuse the saved credentials.
	// A --register flag always overrides the local file so operators can
	// force re-registration without manually deleting the credentials file.
	if cfg.RegistrationToken == "" {
		if tf, err := LoadTokenFile(cfg.TokenFile); err == nil && tf.JWT != "" {
			slog.Info("loaded existing host credentials", "file", cfg.TokenFile, "host_id", tf.HostID)
			return tf, nil
		}
		return nil, fmt.Errorf("no saved host credentials and no registration token provided (use --register flag)")
	}

	arch := runtime.GOARCH
	cpuCores := int32(runtime.NumCPU())
	memoryMB := getMemoryMB()
	diskGB := getDiskGB()

	reqBody := registerRequest{
		Token:    cfg.RegistrationToken,
		Arch:     arch,
		CPUCores: cpuCores,
		MemoryMB: memoryMB,
		DiskGB:   diskGB,
		Address:  cfg.Address,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal registration request: %w", err)
	}

	url := strings.TrimRight(cfg.CPURL, "/") + "/v1/hosts/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create registration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registration request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read registration response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		var errResp errorResponse
		if err := json.Unmarshal(respBody, &errResp); err == nil {
			return nil, fmt.Errorf("registration failed (%d): %s", resp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("registration failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var regResp authResponse
	if err := json.Unmarshal(respBody, &regResp); err != nil {
		return nil, fmt.Errorf("parse registration response: %w", err)
	}

	if regResp.Token == "" {
		return nil, fmt.Errorf("registration response missing token")
	}

	hostID, err := hostIDFromJWT(regResp.Token)
	if err != nil {
		return nil, fmt.Errorf("extract host ID from JWT: %w", err)
	}

	tf := TokenFile{
		HostID:       hostID,
		JWT:          regResp.Token,
		RefreshToken: regResp.RefreshToken,
		CertPEM:      regResp.CertPEM,
		KeyPEM:       regResp.KeyPEM,
		CACertPEM:    regResp.CACertPEM,
	}
	if err := saveTokenFile(cfg.TokenFile, tf); err != nil {
		return nil, fmt.Errorf("save host credentials: %w", err)
	}
	slog.Info("host registered and credentials saved", "file", cfg.TokenFile, "host_id", hostID)

	return &tf, nil
}

// RefreshCredentials exchanges the refresh token for a new JWT, rotated refresh
// token, and (when mTLS is enabled) a new TLS certificate. The credentials file
// is updated in place. Returns the updated TokenFile.
func RefreshCredentials(ctx context.Context, cpURL, credentialsFilePath string) (*TokenFile, error) {
	tf, err := LoadTokenFile(credentialsFilePath)
	if err != nil {
		return nil, fmt.Errorf("load credentials file: %w", err)
	}
	if tf.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available; host must re-register")
	}

	body, _ := json.Marshal(refreshRequest{RefreshToken: tf.RefreshToken})
	url := strings.TrimRight(cpURL, "/") + "/v1/hosts/auth/refresh"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read refresh response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp errorResponse
		if json.Unmarshal(respBody, &errResp) == nil {
			return nil, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, errResp.Error.Message)
		}
		return nil, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var refResp authResponse
	if err := json.Unmarshal(respBody, &refResp); err != nil {
		return nil, fmt.Errorf("parse refresh response: %w", err)
	}

	tf.JWT = refResp.Token
	tf.RefreshToken = refResp.RefreshToken
	if refResp.CertPEM != "" {
		tf.CertPEM = refResp.CertPEM
		tf.KeyPEM = refResp.KeyPEM
		tf.CACertPEM = refResp.CACertPEM
	}
	if err := saveTokenFile(credentialsFilePath, *tf); err != nil {
		return nil, fmt.Errorf("save refreshed credentials: %w", err)
	}

	slog.Info("host credentials refreshed", "host_id", tf.HostID)
	return tf, nil
}

// StartHeartbeat launches a background goroutine that sends periodic heartbeats
// to the control plane. It runs until the context is cancelled.
//
// On 401/403: the heartbeat loop attempts to refresh credentials. If the refresh
// also fails (expired refresh token), it calls pauseAll and stops.
//
// On repeated network failures (3 consecutive), it calls pauseAll but keeps
// retrying — the connection may recover and the host should resume heartbeating.
//
// onDeleted is called when CP returns 404, meaning this host record was deleted.
// The credentials file is removed before calling onDeleted so subsequent starts
// prompt for a new registration token.
//
// onCredsRefreshed is called after a successful credential refresh (JWT + cert).
// It may be nil. The caller uses it to hot-swap the agent's TLS certificate.
func StartHeartbeat(ctx context.Context, cpURL, credentialsFilePath, hostID string, interval time.Duration, pauseAll func(), onDeleted func(), onCredsRefreshed func(*TokenFile)) {
	client := &http.Client{Timeout: 10 * time.Second}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		consecutiveFailures := 0
		pausedDueToFailure := false
		currentJWT := ""

		// Load the current JWT from the credentials file.
		if tf, err := LoadTokenFile(credentialsFilePath); err == nil {
			currentJWT = tf.JWT
		}

		// beat sends one heartbeat. Returns true if the loop should stop.
		beat := func() (stop bool) {
			url := strings.TrimRight(cpURL, "/") + "/v1/hosts/" + hostID + "/heartbeat"
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
			if err != nil {
				slog.Warn("heartbeat: failed to create request", "error", err)
				return false
			}
			req.Header.Set("X-Host-Token", currentJWT)

			resp, err := client.Do(req)
			if err != nil {
				consecutiveFailures++
				slog.Warn("heartbeat: request failed", "error", err, "consecutive_failures", consecutiveFailures)
				if consecutiveFailures >= 3 && !pausedDueToFailure {
					slog.Error("heartbeat: CP unreachable after 3 failures — pausing all sandboxes")
					if pauseAll != nil {
						pauseAll()
					}
					pausedDueToFailure = true
				}
				return false
			}
			resp.Body.Close()

			switch resp.StatusCode {
			case http.StatusNoContent:
				if consecutiveFailures > 0 || pausedDueToFailure {
					slog.Info("heartbeat: CP connection restored")
				}
				consecutiveFailures = 0
				pausedDueToFailure = false

			case http.StatusUnauthorized, http.StatusForbidden:
				slog.Warn("heartbeat: JWT rejected — attempting credentials refresh")
				newCreds, refreshErr := RefreshCredentials(ctx, cpURL, credentialsFilePath)
				if refreshErr != nil {
					slog.Error("heartbeat: credentials refresh failed — pausing all sandboxes; manual re-registration required",
						"error", refreshErr)
					if pauseAll != nil && !pausedDueToFailure {
						pauseAll()
						pausedDueToFailure = true
					}
					// Stop the heartbeat loop — operator must re-register.
					return true
				}
				currentJWT = newCreds.JWT
				slog.Info("heartbeat: credentials refreshed successfully")
				if onCredsRefreshed != nil {
					onCredsRefreshed(newCreds)
				}

			case http.StatusNotFound:
				slog.Error("heartbeat: host no longer exists in CP — host was deleted; removing credentials file and exiting")
				if err := os.Remove(credentialsFilePath); err != nil && !os.IsNotExist(err) {
					slog.Warn("heartbeat: failed to remove credentials file", "error", err)
				}
				if onDeleted != nil {
					onDeleted()
				}
				return true

			default:
				slog.Warn("heartbeat: unexpected status", "status", resp.StatusCode)
			}
			return false
		}

		// Send an immediate heartbeat on startup so the CP sees the host as
		// online without waiting for the first ticker tick.
		if beat() {
			return
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if beat() {
					return
				}
			}
		}
	}()
}

// HostIDFromToken extracts the host_id claim from a host JWT without
// verifying the signature (the agent doesn't have the signing secret).
func HostIDFromToken(token string) (string, error) {
	return hostIDFromJWT(token)
}

// hostIDFromJWT is the internal implementation used by both HostIDFromToken and
// the credentials file loader.
func hostIDFromJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid JWT format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode JWT payload: %w", err)
	}
	var claims struct {
		HostID string `json:"host_id"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("parse JWT claims: %w", err)
	}
	if claims.HostID == "" {
		return "", fmt.Errorf("host_id claim missing from token")
	}
	return claims.HostID, nil
}

// getMemoryMB returns total system memory in MB.
func getMemoryMB() int32 {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return 0
	}
	return int32(info.Totalram * uint64(info.Unit) / (1024 * 1024))
}

// getDiskGB returns total disk space of the root filesystem in GB.
func getDiskGB() int32 {
	var stat unix.Statfs_t
	if err := unix.Statfs("/", &stat); err != nil {
		return 0
	}
	return int32(stat.Blocks * uint64(stat.Bsize) / (1024 * 1024 * 1024))
}
