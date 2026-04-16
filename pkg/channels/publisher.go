package channels

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/pkg/events"
)

const streamKey = "wrenn:events"

// Publisher pushes events onto the Redis stream for the dispatcher to consume.
type Publisher struct {
	rdb *redis.Client
}

// NewPublisher constructs an event publisher.
func NewPublisher(rdb *redis.Client) *Publisher {
	return &Publisher{rdb: rdb}
}

// Publish serializes the event and appends it to the global stream.
// Fire-and-forget: failures are logged, never propagated.
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
}
