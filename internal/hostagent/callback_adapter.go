package hostagent

import (
	"git.omukk.dev/wrenn/wrenn/internal/sandbox"
)

// callbackAdapter adapts CallbackSender to satisfy sandbox.EventSender.
type callbackAdapter struct {
	sender *CallbackSender
}

// NewEventSender wraps a CallbackSender as a sandbox.EventSender.
func NewEventSender(sender *CallbackSender) sandbox.EventSender {
	return &callbackAdapter{sender: sender}
}

func (a *callbackAdapter) SendAsync(event sandbox.LifecycleEvent) {
	a.sender.SendAsync(CallbackEvent{
		Event:     event.Event,
		SandboxID: event.SandboxID,
	})
}
