package sandbox

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// ConnTracker tracks active proxy connections for a single sandbox and
// provides a drain mechanism for pre-pause graceful shutdown.
// It is safe for concurrent use.
//
// Internally we do not use sync.WaitGroup because Wait cannot be interrupted
// — a stuck handler would pin the waiter goroutine forever. Instead we keep
// an explicit counter guarded by mu plus a zeroCh that is closed when the
// counter transitions to 0, allowing Drain/ForceClose to select on it
// alongside cancellation and timeout signals without spawning helper
// goroutines that could leak across Reset boundaries.
type ConnTracker struct {
	draining atomic.Bool

	mu     sync.Mutex
	count  int
	zeroCh chan struct{} // closed when count drops to 0; recreated on next Acquire

	// cancelMu protects cancelDrain so Reset can signal a timed-out Drain
	// to exit early.
	cancelMu    sync.Mutex
	cancelDrain chan struct{}

	// ctx is cancelled by ForceClose to abort all in-flight proxy requests.
	// Initialized lazily on first Acquire; replaced by Reset after a failed
	// pause so new connections get a fresh, non-cancelled context.
	ctxMu  sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
}

// ensureCtx lazily initializes the cancellable context.
func (t *ConnTracker) ensureCtx() {
	if t.ctx == nil {
		t.ctx, t.cancel = context.WithCancel(context.Background())
	}
}

// Acquire registers one in-flight connection. Returns false if the tracker
// is already draining; the caller must not call Release in that case.
func (t *ConnTracker) Acquire() bool {
	if t.draining.Load() {
		return false
	}
	t.mu.Lock()
	// Re-check under mu so a concurrent Drain that flipped draining cannot
	// race past us with the counter already incremented.
	if t.draining.Load() {
		t.mu.Unlock()
		return false
	}
	t.count++
	if t.count == 1 {
		t.zeroCh = make(chan struct{})
	}
	t.mu.Unlock()
	return true
}

// Context returns a context that is cancelled when ForceClose is called.
// Proxy handlers should derive their request context from this so that
// force-close during pause aborts in-flight proxied requests.
func (t *ConnTracker) Context() context.Context {
	t.ctxMu.Lock()
	defer t.ctxMu.Unlock()
	t.ensureCtx()
	return t.ctx
}

// Release marks one connection as complete. Must be called exactly once
// per successful Acquire.
func (t *ConnTracker) Release() {
	t.mu.Lock()
	t.count--
	if t.count == 0 && t.zeroCh != nil {
		close(t.zeroCh)
		t.zeroCh = nil
	}
	t.mu.Unlock()
}

// waitDrain returns a channel that closes when the in-flight count is zero,
// or a closed channel immediately if there's nothing in flight.
func (t *ConnTracker) waitDrain() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.count == 0 {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return t.zeroCh
}

// Drain marks the tracker as draining (all future Acquire calls return
// false) and waits up to timeout for in-flight connections to finish.
// Returns when the count hits 0, Reset is called, or the timeout fires —
// whichever happens first. No goroutine is leaked on timeout.
func (t *ConnTracker) Drain(timeout time.Duration) {
	t.draining.Store(true)

	cancel := make(chan struct{})
	t.cancelMu.Lock()
	t.cancelDrain = cancel
	t.cancelMu.Unlock()

	select {
	case <-t.waitDrain():
	case <-cancel:
	case <-time.After(timeout):
	}
}

// ForceClose cancels all in-flight proxy connections by cancelling the
// shared context. Connections whose request context derives from Context()
// will see their requests aborted, causing the proxy handler to return
// and call Release(). Waits briefly for connections to actually release.
func (t *ConnTracker) ForceClose() {
	t.ctxMu.Lock()
	if t.cancel != nil {
		t.cancel()
	}
	t.ctxMu.Unlock()

	select {
	case <-t.waitDrain():
	case <-time.After(2 * time.Second):
	}
}

// Reset re-enables the tracker after a failed drain. This allows the
// sandbox to accept proxy connections again if the pause operation fails
// and the VM is resumed. It also signals any lingering Drain to exit and
// creates a fresh context for new connections.
func (t *ConnTracker) Reset() {
	t.cancelMu.Lock()
	if t.cancelDrain != nil {
		select {
		case <-t.cancelDrain:
			// Already closed.
		default:
			close(t.cancelDrain)
		}
		t.cancelDrain = nil
	}
	t.cancelMu.Unlock()

	t.ctxMu.Lock()
	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.ctxMu.Unlock()

	t.draining.Store(false)
}
