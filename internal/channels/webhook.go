package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// WebhookDelivery delivers events to webhook URLs with HMAC signing.
type WebhookDelivery struct {
	client *http.Client
}

// NewWebhookDelivery constructs a webhook delivery client.
func NewWebhookDelivery() *WebhookDelivery {
	return &WebhookDelivery{
		client: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Deliver signs and POSTs the event payload to the configured URL.
func (d *WebhookDelivery) Deliver(ctx context.Context, targetURL, secret string, payload []byte) error {
	timestamp := time.Now().UTC().Format(time.RFC3339)
	deliveryID := uuid.New().String()

	// Compute HMAC-SHA256: sign over "timestamp.body".
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "." + string(payload)))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WRENN-SIGNATURE", signature)
	req.Header.Set("X-Wrenn-Delivery", deliveryID)
	req.Header.Set("X-Wrenn-Timestamp", timestamp)

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("http post: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
