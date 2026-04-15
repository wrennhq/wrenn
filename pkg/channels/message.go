package channels

import (
	"fmt"
	"strings"

	"git.omukk.dev/wrenn/wrenn/pkg/events"
)

// FormatMessage produces a human-readable notification string containing
// the event summary, resource details, actor, and timestamp.
func FormatMessage(e events.Event) string {
	var b strings.Builder

	b.WriteString(formatSummary(e))
	fmt.Fprintf(&b, "\n\nEvent: %s", e.Event)
	fmt.Fprintf(&b, "\nResource: %s %s", e.Resource.Type, e.Resource.ID)
	fmt.Fprintf(&b, "\nActor: %s", formatActor(e.Actor))
	fmt.Fprintf(&b, "\nTeam: %s", e.TeamID)
	fmt.Fprintf(&b, "\nTime: %s", e.Timestamp)

	return b.String()
}

func formatSummary(e events.Event) string {
	switch e.Event {
	case events.CapsuleCreated:
		return fmt.Sprintf("Capsule %s created", e.Resource.ID)
	case events.CapsuleRunning:
		return fmt.Sprintf("Capsule %s is running", e.Resource.ID)
	case events.CapsulePaused:
		return fmt.Sprintf("Capsule %s paused", e.Resource.ID)
	case events.CapsuleDestroyed:
		return fmt.Sprintf("Capsule %s destroyed", e.Resource.ID)
	case events.SnapshotCreated:
		return fmt.Sprintf("Template snapshot %s created", e.Resource.ID)
	case events.SnapshotDeleted:
		return fmt.Sprintf("Template snapshot %s deleted", e.Resource.ID)
	case events.HostUp:
		return fmt.Sprintf("Host %s is up", e.Resource.ID)
	case events.HostDown:
		return fmt.Sprintf("Host %s is down", e.Resource.ID)
	default:
		return fmt.Sprintf("%s %s", e.Resource.Type, e.Resource.ID)
	}
}

func formatActor(a events.Actor) string {
	switch a.Type {
	case events.ActorSystem:
		return "system"
	case events.ActorUser:
		if a.Name != "" {
			return fmt.Sprintf("%s (%s)", a.Name, a.ID)
		}
		return a.ID
	case events.ActorAPIKey:
		if a.Name != "" {
			return fmt.Sprintf("api_key %s (%s)", a.Name, a.ID)
		}
		return fmt.Sprintf("api_key %s", a.ID)
	default:
		return string(a.Type)
	}
}
