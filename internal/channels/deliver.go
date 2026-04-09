package channels

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/containrrr/shoutrrr"

	"git.omukk.dev/wrenn/sandbox/internal/events"
)

// Deliver sends a notification to a single provider with the given config.
// For webhooks it uses HMAC-signed HTTP POST; for all others it uses shoutrrr.
func Deliver(ctx context.Context, provider string, config map[string]string, e events.Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	if provider == "webhook" {
		wh := NewWebhookDelivery()
		return wh.Deliver(ctx, config["url"], config["secret"], payload)
	}

	shoutrrrURL, err := ShoutrrrURL(provider, config)
	if err != nil {
		return fmt.Errorf("build shoutrrr URL: %w", err)
	}

	msg := FormatMessage(e)
	if err := shoutrrr.Send(shoutrrrURL, msg); err != nil {
		return fmt.Errorf("shoutrrr send: %w", err)
	}
	return nil
}
