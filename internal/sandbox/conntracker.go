package sandbox

import (
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

// Reset re-enables the tracker after a failed drain. This allows the
// sandbox to accept proxy connections again if the pause operation fails
// and the VM is resumed. It also cancels any lingering Drain goroutine.
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

	t.draining.Store(false)
}
