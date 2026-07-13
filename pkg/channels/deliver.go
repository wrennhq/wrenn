package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/containrrr/shoutrrr"

	"git.omukk.dev/wrenn/wrenn/pkg/events"
)

var shoutrrrGuardOnce sync.Once

// InstallSSRFDialGuard installs the dial-time SSRF guard onto the default HTTP
// transport that shoutrrr providers dial through.
//
// shoutrrr's providers (notably matrix, whose homeserver_url is a fully
// caller-controlled connect host) send via http.Get / http.Post — i.e.
// http.DefaultClient / http.DefaultTransport — and expose no hook to inject a
// custom client. Parse-time URL validation alone is defeated by DNS rebinding
// (resolve to a public IP at validation, rebind to a private IP before
// delivery). Cloning the default transport and attaching the same Control hook
// the webhook client uses re-checks the resolved IP at connect time, closing
// the rebind window for every shoutrrr provider at once.
//
// Idempotent (sync.Once) and a no-op when allowPrivate is set (self-hosted
// deployments that intentionally deliver to internal endpoints). Host-agent
// traffic is unaffected — the host client pool pins its own transports and
// never dials through http.DefaultTransport.
func InstallSSRFDialGuard(allowPrivate bool) {
	if allowPrivate {
		return
	}
	shoutrrrGuardOnce.Do(func() {
		tr, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return
		}
		guarded := tr.Clone()
		guarded.DialContext = dialContext(false)
		http.DefaultTransport = guarded
	})
}

// Deliver sends a notification to a single provider with the given config.
// For webhooks it uses HMAC-signed HTTP POST; for all others it uses shoutrrr.
// allowPrivate relaxes the SSRF guard on the webhook client for self-hosted
// deployments that deliver to internal endpoints.
func Deliver(ctx context.Context, provider string, config map[string]string, e events.Event, allowPrivate bool) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	if provider == "webhook" {
		wh := NewWebhookDelivery(allowPrivate)
		return wh.Deliver(ctx, config["url"], config["secret"], payload)
	}

	shoutrrrURL, err := ShoutrrrURL(provider, config)
	if err != nil {
		return fmt.Errorf("build shoutrrr URL: %w", err)
	}

	// Re-check the resolved IP at connect time for every shoutrrr provider
	// (matrix's homeserver is caller-controlled). Idempotent; startup also
	// installs it, this covers direct callers and tests.
	InstallSSRFDialGuard(allowPrivate)

	msg := FormatMessage(e)
	if err := shoutrrr.Send(shoutrrrURL, msg); err != nil {
		return fmt.Errorf("shoutrrr send: %w", err)
	}
	return nil
}
