package channels

import (
	"fmt"

	"git.omukk.dev/wrenn/sandbox/internal/events"
)

// FormatMessage produces a compact notification string for chat providers.
func FormatMessage(e events.Event) string {
	switch e.Event {
	case events.CapsuleCreated:
		return fmt.Sprintf("[%s] Capsule %s created", e.Event, e.Resource.ID)
	case events.CapsuleRunning:
		return fmt.Sprintf("[%s] Capsule %s is running", e.Event, e.Resource.ID)
	case events.CapsulePaused:
		return fmt.Sprintf("[%s] Capsule %s paused", e.Event, e.Resource.ID)
	case events.CapsuleDestroyed:
		return fmt.Sprintf("[%s] Capsule %s destroyed", e.Event, e.Resource.ID)
	case events.SnapshotCreated:
		return fmt.Sprintf("[%s] Template snapshot %s created", e.Event, e.Resource.ID)
	case events.SnapshotDeleted:
		return fmt.Sprintf("[%s] Template snapshot %s deleted", e.Event, e.Resource.ID)
	case events.HostUp:
		return fmt.Sprintf("[%s] Host %s is up", e.Event, e.Resource.ID)
	case events.HostDown:
		return fmt.Sprintf("[%s] Host %s is down", e.Event, e.Resource.ID)
	default:
		return fmt.Sprintf("[%s] %s %s", e.Event, e.Resource.Type, e.Resource.ID)
	}
}
