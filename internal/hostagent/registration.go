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
	Host  json.RawMessage `json:"host"`
	Token string          `json:"token"`
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Register calls the control plane to register this host agent and persists
// the returned JWT to disk. Returns the host JWT token string.
func Register(ctx context.Context, cfg RegistrationConfig) (string, error) {
	// Check if we already have a saved token.
	if data, err := os.ReadFile(cfg.TokenFile); err == nil {
		token := strings.TrimSpace(string(data))
		if token != "" {
			slog.Info("loaded existing host token", "file", cfg.TokenFile)
			return token, nil
		}
	}

	if cfg.RegistrationToken == "" {
		return "", fmt.Errorf("no saved host token and no registration token provided")
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

	// Persist the token to disk for subsequent startups.
	if err := os.WriteFile(cfg.TokenFile, []byte(regResp.Token), 0600); err != nil {
		return "", fmt.Errorf("save host token: %w", err)
	}
	slog.Info("host registered and token saved", "file", cfg.TokenFile)

	return regResp.Token, nil
}

// StartHeartbeat launches a background goroutine that sends periodic heartbeats
// to the control plane. It runs until the context is cancelled.
func StartHeartbeat(ctx context.Context, cpURL, hostID, hostToken string, interval time.Duration) {
	url := strings.TrimRight(cpURL, "/") + "/v1/hosts/" + hostID + "/heartbeat"
	client := &http.Client{Timeout: 10 * time.Second}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
				if err != nil {
					slog.Warn("heartbeat: failed to create request", "error", err)
					continue
				}
				req.Header.Set("X-Host-Token", hostToken)

				resp, err := client.Do(req)
				if err != nil {
					slog.Warn("heartbeat: request failed", "error", err)
					continue
				}
				resp.Body.Close()

				if resp.StatusCode != http.StatusNoContent {
					slog.Warn("heartbeat: unexpected status", "status", resp.StatusCode)
				}
			}
		}
	}()
}

// HostIDFromToken extracts the host_id claim from a host JWT without
// verifying the signature (the agent doesn't have the signing secret).
func HostIDFromToken(token string) (string, error) {
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
