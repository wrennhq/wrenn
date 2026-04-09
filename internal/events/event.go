package events

import (
	"context"
	"time"
)

// EventPublisher pushes events onto the notification stream.
// Satisfied by *channels.Publisher.
type EventPublisher interface {
	Publish(ctx context.Context, e Event)
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

// Resource identifies the object the event relates to.
type Resource struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Event is the canonical notification payload published to the Redis stream
// and delivered to channel subscribers.
type Event struct {
	Event     string   `json:"event"`
	Timestamp string   `json:"timestamp"`
	TeamID    string   `json:"team_id"`
	Actor     Actor    `json:"actor"`
	Resource  Resource `json:"resource"`
}

// Event type constants.
const (
	CapsuleCreated   = "capsule.created"
	CapsuleRunning   = "capsule.running"
	CapsulePaused    = "capsule.paused"
	CapsuleDestroyed = "capsule.destroyed"
	SnapshotCreated  = "template.snapshot.created"
	SnapshotDeleted  = "template.snapshot.deleted"
	HostUp           = "host.up"
	HostDown         = "host.down"
)

// AllEventTypes is the complete set of valid event type strings.
var AllEventTypes = []string{
	CapsuleCreated,
	CapsuleRunning,
	CapsulePaused,
	CapsuleDestroyed,
	SnapshotCreated,
	SnapshotDeleted,
	HostUp,
	HostDown,
}

// Now returns the current time formatted for event timestamps.
func Now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
