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

// tokenFile is the JSON format persisted to AGENT_FILES_ROOTDIR/host.jwt.
type tokenFile struct {
	HostID       string `json:"host_id"`
	JWT          string `json:"jwt"`
	RefreshToken string `json:"refresh_token"`
}

// RegistrationConfig holds the configuration for host registration.
type RegistrationConfig struct {
	CPURL             string // Control plane base URL (e.g., http://localhost:8000)
	RegistrationToken string // One-time registration token from the control plane
	TokenFile         string // Path to persist the host JWT after registration
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

type registerResponse struct {
	Host         json.RawMessage `json:"host"`
	Token        string          `json:"token"`
	RefreshToken string          `json:"refresh_token"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type refreshResponse struct {
	Host         json.RawMessage `json:"host"`
	Token        string          `json:"token"`
	RefreshToken string          `json:"refresh_token"`
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// loadTokenFile reads and parses the persisted token file.
func loadTokenFile(path string) (*tokenFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Support legacy format (raw JWT string) for backwards compatibility.
	trimmed := strings.TrimSpace(string(data))
	if !strings.HasPrefix(trimmed, "{") {
		// Old format: just the JWT, no refresh token.
		hostID, _ := hostIDFromJWT(trimmed)
		return &tokenFile{HostID: hostID, JWT: trimmed}, nil
	}
	var tf tokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("parse token file: %w", err)
	}
	return &tf, nil
}

// saveTokenFile writes the token file as JSON with 0600 permissions.
func saveTokenFile(path string, tf tokenFile) error {
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal token file: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

// Register calls the control plane to register this host agent and persists
// the returned JWT and refresh token to disk. Returns the host JWT token string.
func Register(ctx context.Context, cfg RegistrationConfig) (string, error) {
	// Check if we already have a saved token.
	if tf, err := loadTokenFile(cfg.TokenFile); err == nil && tf.JWT != "" {
		slog.Info("loaded existing host token", "file", cfg.TokenFile, "host_id", tf.HostID)
		return tf.JWT, nil
	}

	if cfg.RegistrationToken == "" {
		return "", fmt.Errorf("no saved host token and no registration token provided (use --register flag)")
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
		return "", fmt.Errorf("marshal registration request: %w", err)
	}

	url := strings.TrimRight(cfg.CPURL, "/") + "/v1/hosts/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create registration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("registration request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read registration response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		var errResp errorResponse
		if err := json.Unmarshal(respBody, &errResp); err == nil {
			return "", fmt.Errorf("registration failed (%d): %s", resp.StatusCode, errResp.Error.Message)
		}
		return "", fmt.Errorf("registration failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var regResp registerResponse
	if err := json.Unmarshal(respBody, &regResp); err != nil {
		return "", fmt.Errorf("parse registration response: %w", err)
	}

	if regResp.Token == "" {
		return "", fmt.Errorf("registration response missing token")
	}

	hostID, err := hostIDFromJWT(regResp.Token)
	if err != nil {
		return "", fmt.Errorf("extract host ID from JWT: %w", err)
	}

	// Persist JWT + refresh token.
	tf := tokenFile{
		HostID:       hostID,
		JWT:          regResp.Token,
		RefreshToken: regResp.RefreshToken,
	}
	if err := saveTokenFile(cfg.TokenFile, tf); err != nil {
		return "", fmt.Errorf("save host token: %w", err)
	}
	slog.Info("host registered and token saved", "file", cfg.TokenFile, "host_id", hostID)

	return regResp.Token, nil
}

// RefreshJWT exchanges the refresh token for a new JWT + rotated refresh token.
// It reads and updates the token file in place.
func RefreshJWT(ctx context.Context, cpURL, tokenFilePath string) (string, error) {
	tf, err := loadTokenFile(tokenFilePath)
	if err != nil {
		return "", fmt.Errorf("load token file: %w", err)
	}
	if tf.RefreshToken == "" {
		return "", fmt.Errorf("no refresh token available; host must re-register")
	}

	body, _ := json.Marshal(refreshRequest{RefreshToken: tf.RefreshToken})
	url := strings.TrimRight(cpURL, "/") + "/v1/hosts/auth/refresh"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		var errResp errorResponse
		if json.Unmarshal(respBody, &errResp) == nil {
			return "", fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, errResp.Error.Message)
		}
		return "", fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var refResp refreshResponse
	if err := json.Unmarshal(respBody, &refResp); err != nil {
		return "", fmt.Errorf("parse refresh response: %w", err)
	}

	tf.JWT = refResp.Token
	tf.RefreshToken = refResp.RefreshToken
	if err := saveTokenFile(tokenFilePath, *tf); err != nil {
		return "", fmt.Errorf("save refreshed token: %w", err)
	}

	slog.Info("host JWT refreshed", "host_id", tf.HostID)
	return refResp.Token, nil
}

// StartHeartbeat launches a background goroutine that sends periodic heartbeats
// to the control plane. It runs until the context is cancelled.
//
// On 401/403: the heartbeat loop attempts to refresh the JWT. If the refresh
// also fails (expired refresh token), it calls pauseAll and stops.
//
// On repeated network failures (3 consecutive), it calls pauseAll but keeps
// retrying — the connection may recover and the host should resume heartbeating.
func StartHeartbeat(ctx context.Context, cpURL, tokenFilePath, hostID string, interval time.Duration, pauseAll func()) {
	client := &http.Client{Timeout: 10 * time.Second}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		consecutiveFailures := 0
		pausedDueToFailure := false
		currentJWT := ""

		// Load the current JWT from disk.
		if tf, err := loadTokenFile(tokenFilePath); err == nil {
			currentJWT = tf.JWT
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				url := strings.TrimRight(cpURL, "/") + "/v1/hosts/" + hostID + "/heartbeat"
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
				if err != nil {
					slog.Warn("heartbeat: failed to create request", "error", err)
					continue
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
					continue
				}
				resp.Body.Close()

				switch resp.StatusCode {
				case http.StatusNoContent:
					// Success.
					if consecutiveFailures > 0 || pausedDueToFailure {
						slog.Info("heartbeat: CP connection restored")
					}
					consecutiveFailures = 0
					pausedDueToFailure = false

				case http.StatusUnauthorized, http.StatusForbidden:
					slog.Warn("heartbeat: JWT rejected — attempting token refresh")
					newJWT, refreshErr := RefreshJWT(ctx, cpURL, tokenFilePath)
					if refreshErr != nil {
						slog.Error("heartbeat: JWT refresh failed — pausing all sandboxes; manual re-registration required",
							"error", refreshErr)
						if pauseAll != nil && !pausedDueToFailure {
							pauseAll()
							pausedDueToFailure = true
						}
						// Stop the heartbeat loop — operator must re-register.
						return
					}
					currentJWT = newJWT
					slog.Info("heartbeat: JWT refreshed successfully")

				default:
					slog.Warn("heartbeat: unexpected status", "status", resp.StatusCode)
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
// the token file loader.
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
