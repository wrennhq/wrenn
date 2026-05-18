package channels

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/pkg/events"
)

const (
	streamKey        = "wrenn:events"
	ssePubSubChannel = "wrenn:sse"
)

// Publisher pushes events onto the Redis stream for the dispatcher to consume.
type Publisher struct {
	rdb *redis.Client
}

// NewPublisher constructs an event publisher.
func NewPublisher(rdb *redis.Client) *Publisher {
	return &Publisher{rdb: rdb}
}

// Publish serializes the event, appends it to the durable Redis stream
// (consumed by channel dispatcher for webhook/telegram delivery), and
// mirrors it on the SSE Pub/Sub channel for the dashboard. Fire-and-forget.
func (p *Publisher) Publish(ctx context.Context, e events.Event) {
	payload, err := json.Marshal(e)
	if err != nil {
		slog.Warn("channels: failed to marshal event", "event", e.Event, "error", err)
		return
	}

	if err := p.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey,
		MaxLen: 10000,
		Approx: true,
		Values: map[string]interface{}{
			"payload": string(payload),
		},
	}).Err(); err != nil {
		slog.Warn("channels: failed to publish event", "event", e.Event, "error", err)
	}

	if err := p.rdb.Publish(ctx, ssePubSubChannel, string(payload)).Err(); err != nil {
		slog.Warn("channels: failed to publish SSE event", "event", e.Event, "error", err)
	}
}

// PublishTransient mirrors the event on the SSE Pub/Sub channel only — no
// durable stream write, no channel dispatch. Used for ephemeral UI signals
// (status transitions during start/pause/resume) that should reach the
// dashboard live but must not be delivered to webhook/telegram subscribers.
func (p *Publisher) PublishTransient(ctx context.Context, e events.Event) {
	payload, err := json.Marshal(e)
	if err != nil {
		slog.Warn("channels: failed to marshal transient event", "event", e.Event, "error", err)
		return
	}

	if err := p.rdb.Publish(ctx, ssePubSubChannel, string(payload)).Err(); err != nil {
		slog.Warn("channels: failed to publish transient SSE event", "event", e.Event, "error", err)
	}
}
