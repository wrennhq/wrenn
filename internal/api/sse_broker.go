package api

import (
	"encoding/json"
	"log/slog"
	"sync"
	"sync/atomic"
)

const sseChannelBuffer = 32

type sseMessage struct {
	EventType string
	Data      json.RawMessage
}

type sseSubscriber struct {
	teamID  string
	isAdmin bool
	ch      chan sseMessage
}

// SSEBroker is an in-process fan-out hub that dispatches events to connected
// SSE clients, filtering by team ownership.
type SSEBroker struct {
	mu          sync.RWMutex
	nextID      atomic.Uint64
	subscribers map[uint64]*sseSubscriber
}

// NewSSEBroker constructs a broker with no subscribers.
func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		subscribers: make(map[uint64]*sseSubscriber),
	}
}

// Subscribe registers a new SSE client. Returns a subscriber ID (for
// Unsubscribe) and a receive-only channel that delivers filtered events.
func (b *SSEBroker) Subscribe(teamID string, isAdmin bool) (uint64, <-chan sseMessage) {
	id := b.nextID.Add(1)
	ch := make(chan sseMessage, sseChannelBuffer)
	sub := &sseSubscriber{teamID: teamID, isAdmin: isAdmin, ch: ch}

	b.mu.Lock()
	b.subscribers[id] = sub
	b.mu.Unlock()

	slog.Debug("sse: client subscribed", "sub_id", id, "team_id", teamID, "admin", isAdmin)
	return id, ch
}

// Unsubscribe removes a client. The handler loop exits via context cancellation;
// the channel is not closed here to avoid send-on-closed-channel races with Dispatch.
func (b *SSEBroker) Unsubscribe(id uint64) {
	b.mu.Lock()
	delete(b.subscribers, id)
	b.mu.Unlock()
	slog.Debug("sse: client unsubscribed", "sub_id", id)
}

// Dispatch fans out an event to all matching subscribers. Admin subscribers
// receive all events; team subscribers only receive events for their team.
// Non-blocking: events are dropped for slow consumers.
func (b *SSEBroker) Dispatch(eventType string, teamID string, data json.RawMessage) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	msg := sseMessage{EventType: eventType, Data: data}
	for id, sub := range b.subscribers {
		if !sub.isAdmin && sub.teamID != teamID {
			continue
		}
		select {
		case sub.ch <- msg:
		default:
			slog.Warn("sse: dropped event for slow consumer", "sub_id", id, "event", eventType)
		}
	}
}
