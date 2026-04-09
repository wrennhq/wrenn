package channels

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/events"
	"git.omukk.dev/wrenn/wrenn/internal/id"
)

const (
	groupName    = "wrenn-channels-v1"
	consumerName = "cp-0"
)

// Dispatcher consumes events from the Redis stream and delivers them
// to matching notification channels.
type Dispatcher struct {
	rdb     *redis.Client
	db      *db.Queries
	encKey  [32]byte
	webhook *WebhookDelivery
}

// NewDispatcher constructs an event dispatcher.
func NewDispatcher(rdb *redis.Client, queries *db.Queries, encKey [32]byte) *Dispatcher {
	return &Dispatcher{
		rdb:     rdb,
		db:      queries,
		encKey:  encKey,
		webhook: NewWebhookDelivery(),
	}
}

// Start launches the consumer goroutine. Returns when ctx is cancelled.
func (d *Dispatcher) Start(ctx context.Context) {
	go d.run(ctx)
}

func (d *Dispatcher) run(ctx context.Context) {
	// Create consumer group idempotently. "$" means only new messages.
	err := d.rdb.XGroupCreateMkStream(ctx, streamKey, groupName, "$").Err()
	if err != nil && !isGroupExistsError(err) {
		slog.Error("channels: failed to create consumer group", "error", err)
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		streams, err := d.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    groupName,
			Consumer: consumerName,
			Streams:  []string{streamKey, ">"},
			Count:    10,
			Block:    5 * time.Second,
		}).Result()

		if err != nil {
			if err == redis.Nil || ctx.Err() != nil {
				continue
			}
			slog.Warn("channels: xreadgroup error", "error", err)
			time.Sleep(1 * time.Second)
			continue
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				d.handleMessage(ctx, msg)
			}
		}
	}
}

func (d *Dispatcher) handleMessage(ctx context.Context, msg redis.XMessage) {
	defer func() {
		if err := d.rdb.XAck(ctx, streamKey, groupName, msg.ID).Err(); err != nil {
			slog.Warn("channels: xack failed", "id", msg.ID, "error", err)
		}
	}()

	payload, ok := msg.Values["payload"].(string)
	if !ok {
		slog.Warn("channels: message missing payload", "id", msg.ID)
		return
	}

	var event events.Event
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		slog.Warn("channels: failed to unmarshal event", "id", msg.ID, "error", err)
		return
	}

	teamID, err := id.ParseTeamID(event.TeamID)
	if err != nil {
		slog.Warn("channels: invalid team ID in event", "team_id", event.TeamID, "error", err)
		return
	}

	channels, err := d.db.ListChannelsForEvent(ctx, db.ListChannelsForEventParams{
		TeamID:    teamID,
		EventType: event.Event,
	})
	if err != nil {
		slog.Warn("channels: failed to list channels for event", "event", event.Event, "error", err)
		return
	}

	for _, ch := range channels {
		d.dispatch(ctx, ch, event)
	}
}

// retryDelays defines the wait durations before each retry attempt.
var retryDelays = []time.Duration{10 * time.Second, 30 * time.Second}

func (d *Dispatcher) dispatch(ctx context.Context, ch db.Channel, e events.Event) {
	config, err := d.decryptConfig(ch.Config)
	if err != nil {
		slog.Warn("channels: failed to decrypt config",
			"channel_id", id.FormatChannelID(ch.ID), "error", err)
		return
	}

	chID := id.FormatChannelID(ch.ID)

	if err := Deliver(ctx, ch.Provider, config, e); err != nil {
		slog.Warn("channels: delivery failed, scheduling retries",
			"channel_id", chID, "provider", ch.Provider, "error", err)
		go d.retryDeliver(ctx, ch.Provider, config, e, chID)
	}
}

func (d *Dispatcher) retryDeliver(ctx context.Context, provider string, config map[string]string, e events.Event, chID string) {
	for i, delay := range retryDelays {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		if err := Deliver(ctx, provider, config, e); err != nil {
			slog.Warn("channels: retry delivery failed",
				"channel_id", chID, "provider", provider,
				"attempt", i+2, "error", err)
			continue
		}
		return
	}
	slog.Error("channels: delivery failed after all retries",
		"channel_id", chID, "provider", provider, "event", e.Event)
}

func (d *Dispatcher) decryptConfig(configJSON []byte) (map[string]string, error) {
	var encrypted map[string]string
	if err := json.Unmarshal(configJSON, &encrypted); err != nil {
		return nil, err
	}

	decrypted := make(map[string]string, len(encrypted))
	for k, v := range encrypted {
		plaintext, err := DecryptSecret(d.encKey, v)
		if err != nil {
			return nil, err
		}
		decrypted[k] = plaintext
	}
	return decrypted, nil
}

func isGroupExistsError(err error) bool {
	return err != nil && err.Error() == "BUSYGROUP Consumer Group name already exists"
}
