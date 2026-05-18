package events

import (
	"context"
	"time"
)

// EventPublisher pushes events onto the notification stream.
// Satisfied by *channels.Publisher.
//
// Publish writes durably (Redis stream + SSE Pub/Sub mirror) and is delivered
// to subscribed channels. Use for actions that users may want webhook/telegram
// notifications about.
//
// PublishTransient writes only to the SSE Pub/Sub mirror — no durable stream,
// no channel delivery. Use for ephemeral UI signals (e.g., status transitions
// while a sandbox is starting/pausing) that should reach the dashboard but
// must not spam subscribers.
type EventPublisher interface {
	Publish(ctx context.Context, e Event)
	PublishTransient(ctx context.Context, e Event)
}

// ActorKind identifies what initiated an event.
type ActorKind string

const (
	ActorUser   ActorKind = "user"
	ActorAPIKey ActorKind = "api_key"
	ActorSystem ActorKind = "system"
)

// Actor describes who triggered an event.
type Actor struct {
	Type ActorKind `json:"type"`
	ID   string    `json:"id,omitempty"`
	Name string    `json:"name,omitempty"`
}

// SystemActor returns the canonical actor for system-initiated events
// (TTL reaper, reconciler-inferred state, cleanup-on-error).
func SystemActor() Actor {
	return Actor{Type: ActorSystem}
}

// Resource identifies the object the event relates to.
type Resource struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Outcome encodes whether an action succeeded or failed.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeError   Outcome = "error"
)

// Event is the canonical notification payload published to the Redis stream
// and delivered to channel subscribers.
//
// Outcome distinguishes success vs. failure for action events. It is empty
// for events with no success/error semantics (state.changed, host.up, host.down).
// Error carries the failure reason when Outcome == OutcomeError.
// Metadata carries event-specific structured context (e.g., reason, from/to
// state for transitions, inferred=true for reconciler-derived events).
type Event struct {
	Event     string            `json:"event"`
	Outcome   Outcome           `json:"outcome,omitempty"`
	Timestamp string            `json:"timestamp"`
	TeamID    string            `json:"team_id"`
	Actor     Actor             `json:"actor"`
	Resource  Resource          `json:"resource"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// Event type constants. Group-level names: subscription matches on Event,
// Outcome is a payload field so webhook recipients can distinguish success
// from failure without separate subscriptions.
const (
	// Durable, subscribable. First boot only (subsequent unpauses are CapsuleResume).
	CapsuleCreate  = "capsule.create"
	CapsulePause   = "capsule.pause"
	CapsuleResume  = "capsule.resume"
	CapsuleDestroy = "capsule.destroy"

	// Durable, subscribable.
	SnapshotCreate = "template.snapshot.create"
	SnapshotDelete = "template.snapshot.delete"

	// Durable, no outcome (binary by name).
	HostUp   = "host.up"
	HostDown = "host.down"

	// Transient (SSE-only via PublishTransient). Not subscribable.
	// Metadata: from, to (sandbox status strings).
	CapsuleStateChanged = "capsule.state.changed"
)

// SubscribableEventTypes is the set of event types users can subscribe to
// via channels (webhook, telegram, shoutrrr). Excludes transient events.
var SubscribableEventTypes = []string{
	CapsuleCreate,
	CapsulePause,
	CapsuleResume,
	CapsuleDestroy,
	SnapshotCreate,
	SnapshotDelete,
	HostUp,
	HostDown,
}

// Now returns the current time formatted for event timestamps.
func Now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
