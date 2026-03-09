package envdclient

import (
	"context"
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
