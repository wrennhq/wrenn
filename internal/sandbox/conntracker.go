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
type ConnTracker struct {
	draining atomic.Bool
	wg       sync.WaitGroup

	// cancelMu protects cancelDrain so Reset can signal a timed-out Drain
	// goroutine to exit, preventing goroutine leaks on repeated pause failures.
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
	t.wg.Add(1)
	// Re-check after Add: Drain may have set draining between our Load
	// and Add. If so, undo the Add and reject the connection.
	if t.draining.Load() {
		t.wg.Done()
		return false
	}
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
	t.wg.Done()
}

// Drain marks the tracker as draining (all future Acquire calls return
// false) and waits up to timeout for in-flight connections to finish.
func (t *ConnTracker) Drain(timeout time.Duration) {
	t.draining.Store(true)

	cancel := make(chan struct{})
	t.cancelMu.Lock()
	t.cancelDrain = cancel
	t.cancelMu.Unlock()

	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-cancel:
		// Reset was called; stop waiting.
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

	// Wait briefly for force-closed connections to call Release().
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}

// Reset re-enables the tracker after a failed drain. This allows the
// sandbox to accept proxy connections again if the pause operation fails
// and the VM is resumed. It also cancels any lingering Drain goroutine
// and creates a fresh context for new connections.
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

	// Replace the cancelled context with a fresh one.
	t.ctxMu.Lock()
	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.ctxMu.Unlock()

	t.draining.Store(false)
}
