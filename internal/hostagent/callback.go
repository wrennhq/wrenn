package hostagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// CallbackEvent is the payload sent to the CP's sandbox event callback endpoint.
type CallbackEvent struct {
	Event     string `json:"event"`
	SandboxID string `json:"sandbox_id"`
	HostID    string `json:"host_id"`
	Timestamp int64  `json:"timestamp"`
}

// CallbackSender sends sandbox lifecycle events to the CP via HTTP POST.
// Used for autonomous agent-side events (auto-pause, auto-destroy) that
// the CP cannot observe through its own RPC goroutines.
type CallbackSender struct {
	cpURL    string
	hostID   string
	credFile string
	client   *http.Client
	mu       sync.RWMutex
	jwt      string
}

// NewCallbackSender creates a callback sender.
func NewCallbackSender(cpURL, credFile, hostID string) *CallbackSender {
	jwt := ""
	if tf, err := LoadTokenFile(credFile); err == nil {
		jwt = tf.JWT
	}
	return &CallbackSender{
		cpURL:    strings.TrimRight(cpURL, "/"),
		hostID:   hostID,
		credFile: credFile,
		client:   &http.Client{Timeout: 10 * time.Second},
		jwt:      jwt,
	}
}

// UpdateJWT refreshes the JWT used for callback authentication.
// Called from the heartbeat's onCredsRefreshed hook.
func (s *CallbackSender) UpdateJWT(jwt string) {
	s.mu.Lock()
	s.jwt = jwt
	s.mu.Unlock()
}

func (s *CallbackSender) getJWT() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.jwt
}

// Send sends a callback event to the CP synchronously with retries.
func (s *CallbackSender) Send(ctx context.Context, ev CallbackEvent) error {
	ev.HostID = s.hostID
	if ev.Timestamp == 0 {
		ev.Timestamp = time.Now().Unix()
	}

	body, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal callback event: %w", err)
	}

	url := s.cpURL + "/v1/hosts/sandbox-events"

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create callback request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Host-Token", s.getJWT())

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if newCreds, refreshErr := RefreshCredentials(ctx, s.cpURL, s.credFile); refreshErr == nil {
				s.UpdateJWT(newCreds.JWT)
			}
			lastErr = fmt.Errorf("callback auth failed: %d", resp.StatusCode)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		lastErr = fmt.Errorf("callback failed: status %d", resp.StatusCode)
	}

	return fmt.Errorf("callback failed after 3 attempts: %w", lastErr)
}

// SendAsync sends a callback event in a background goroutine.
func (s *CallbackSender) SendAsync(ev CallbackEvent) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.Send(ctx, ev); err != nil {
			slog.Warn("callback send failed (reconciler will catch it)", "event", ev.Event, "sandbox_id", ev.SandboxID, "error", err)
		}
	}()
}
