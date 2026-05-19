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
	if e.Outcome != "" {
		fmt.Fprintf(&b, "\nOutcome: %s", e.Outcome)
	}
	fmt.Fprintf(&b, "\nResource: %s %s", e.Resource.Type, e.Resource.ID)
	fmt.Fprintf(&b, "\nActor: %s", formatActor(e.Actor))
	fmt.Fprintf(&b, "\nTeam: %s", e.TeamID)
	fmt.Fprintf(&b, "\nTime: %s", e.Timestamp)
	if e.Error != "" {
		fmt.Fprintf(&b, "\nError: %s", e.Error)
	}
	if reason, ok := e.Metadata["reason"]; ok {
		fmt.Fprintf(&b, "\nReason: %s", reason)
	}

	return b.String()
}

func formatSummary(e events.Event) string {
	failed := e.Outcome == events.OutcomeError
	switch e.Event {
	case events.CapsuleCreate:
		if failed {
			return fmt.Sprintf("Capsule %s failed to create", e.Resource.ID)
		}
		return fmt.Sprintf("Capsule %s created", e.Resource.ID)
	case events.CapsulePause:
		if failed {
			return fmt.Sprintf("Capsule %s failed to pause", e.Resource.ID)
		}
		return fmt.Sprintf("Capsule %s paused", e.Resource.ID)
	case events.CapsuleResume:
		if failed {
			return fmt.Sprintf("Capsule %s failed to resume", e.Resource.ID)
		}
		return fmt.Sprintf("Capsule %s resumed", e.Resource.ID)
	case events.CapsuleDestroy:
		if failed {
			return fmt.Sprintf("Capsule %s failed to destroy", e.Resource.ID)
		}
		return fmt.Sprintf("Capsule %s destroyed", e.Resource.ID)
	case events.SnapshotCreate:
		if failed {
			return fmt.Sprintf("Template snapshot %s failed to create", e.Resource.ID)
		}
		return fmt.Sprintf("Template snapshot %s created", e.Resource.ID)
	case events.SnapshotDelete:
		if failed {
			return fmt.Sprintf("Template snapshot %s failed to delete", e.Resource.ID)
		}
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
