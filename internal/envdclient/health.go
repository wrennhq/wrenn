package envdclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return "", fmt.Errorf("health check returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) == 0 {
		return "", nil // envd may not support version reporting yet
	}

	var data struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", nil // non-JSON response, old envd
	}

	return data.Version, nil
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

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("health check returned %d", resp.StatusCode)
	}

	return nil
}
