package envdclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// WaitUntilReady polls envd's health endpoint until it responds successfully
// or the context is cancelled. It retries every retryInterval.
func (c *Client) WaitUntilReady(ctx context.Context) error {
	const retryInterval = 100 * time.Millisecond

	slog.Info("waiting for envd to be ready", "url", c.healthURL)

	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("envd not ready: %w", ctx.Err())
		case <-ticker.C:
			if err := c.healthCheck(ctx); err == nil {
				slog.Info("envd is ready", "host", c.hostIP)
				return nil
			}
		}
	}
}

// FetchVersion queries envd's health endpoint and returns the reported version.
func (c *Client) FetchVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.healthURL, nil)
	if err != nil {
		return "", fmt.Errorf("build health request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch envd version: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("health check returned %d", resp.StatusCode)
	}

	var data struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("decode version response: %w", err)
	}

	return data.Version, nil
}

// WaitUntilRPCReady polls envd's Connect RPC layer until it responds
// successfully or the context is cancelled. This catches cases where envd's
// HTTP health endpoint works but the Connect protocol layer is not yet
// functional (e.g., after VM snapshot restore).
func (c *Client) WaitUntilRPCReady(ctx context.Context) error {
	const retryInterval = 200 * time.Millisecond

	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("envd RPC not ready: %w", ctx.Err())
		case <-ticker.C:
			if _, err := c.ListProcesses(ctx); err == nil {
				return nil
			}
		}
	}
}

// Activity is envd's liveness snapshot: VM-wide CPU utilisation and IO
// throughput sampled inside the guest. The host activity sampler uses it to
// decide whether a sandbox is doing real work and should keep its TTL fresh.
type Activity struct {
	CPUCount   uint32  `json:"cpu_count"`
	CPUUsedPct float32 `json:"cpu_used_pct"`
	NetBps     uint64  `json:"net_bps"`
	DiskBps    uint64  `json:"disk_bps"`
}

// FetchActivity polls envd's /activity endpoint. The endpoint serves straight
// from in-guest atomics (no syscalls), so it is cheap to call frequently.
func (c *Client) FetchActivity(ctx context.Context) (*Activity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.activityURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build activity request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch envd activity: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("activity check returned %d", resp.StatusCode)
	}

	var data Activity
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode activity response: %w", err)
	}

	return &data, nil
}

// healthCheck sends a single GET /health request to envd.
func (c *Client) healthCheck(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.healthURL, nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned %d", resp.StatusCode)
	}

	return nil
}
