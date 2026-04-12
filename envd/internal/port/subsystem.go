// SPDX-License-Identifier: Apache-2.0
// Modifications by M/S Omukk

package port

import (
	"context"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"git.omukk.dev/wrenn/sandbox/envd/internal/services/cgroups"
)

// PortSubsystem owns the port scanner and forwarder lifecycle.
// It supports stop/restart across Firecracker snapshot/restore cycles.
type PortSubsystem struct {
	logger        *zerolog.Logger
	cgroupManager cgroups.Manager
	period        time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	wg      *sync.WaitGroup // per-cycle WaitGroup; nil when not running
	running bool
}

// NewPortSubsystem creates a new PortSubsystem. Call Start() to begin scanning.
func NewPortSubsystem(logger *zerolog.Logger, cgroupManager cgroups.Manager, period time.Duration) *PortSubsystem {
	return &PortSubsystem{
		logger:        logger,
		cgroupManager: cgroupManager,
		period:        period,
	}
}

// Start creates a fresh scanner and forwarder, launching their goroutines.
// Safe to call multiple times; does nothing if already running.
func (p *PortSubsystem) Start(parentCtx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.running {
		return
	}

	ctx, cancel := context.WithCancel(parentCtx)
	p.cancel = cancel
	p.running = true

	// Allocate a fresh WaitGroup for this lifecycle so a concurrent Stop
	// on the previous cycle's WaitGroup cannot interfere.
	wg := &sync.WaitGroup{}
	p.wg = wg

	scanner := NewScanner(p.period)
	forwarder := NewForwarder(p.logger, scanner, p.cgroupManager)

	wg.Add(2)

	go func() {
		defer wg.Done()
		forwarder.StartForwarding(ctx)
	}()

	go func() {
		defer wg.Done()
		scanner.ScanAndBroadcast(ctx)
	}()
}

// Stop quiesces the scanner and forwarder goroutines and forces a GC cycle
// to put the Go runtime's page allocator in a consistent state before snapshot.
// Blocks until both goroutines have exited. Safe to call if already stopped.
func (p *PortSubsystem) Stop() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}
	cancelFn := p.cancel
	wg := p.wg
	p.cancel = nil
	p.wg = nil
	p.running = false
	p.mu.Unlock()

	cancelFn()
	wg.Wait()

	// Force two GC cycles to ensure all spans are swept and the page
	// allocator summary tree is fully consistent before the VM is frozen.
	runtime.GC()
	runtime.GC()
	debug.FreeOSMemory()
}

// Restart stops the subsystem (if running) and starts it again with a fresh
// scanner and forwarder. Used after snapshot restore via PostInit.
func (p *PortSubsystem) Restart(parentCtx context.Context) {
	p.Stop()
	p.Start(parentCtx)
}
