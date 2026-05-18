package hostagent

import (
	"context"

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

func (a *callbackAdapter) Send(ctx context.Context, event sandbox.LifecycleEvent) error {
	return a.sender.Send(ctx, CallbackEvent{
		Event:     event.Event,
		SandboxID: event.SandboxID,
	})
}
