package vm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// VM represents a running Cloud Hypervisor microVM.
type VM struct {
	Config  VMConfig
	process *process
	client  *chClient
}

// Manager handles the lifecycle of Cloud Hypervisor microVMs.
type Manager struct {
	mu sync.RWMutex
	// vms tracks running VMs by sandbox ID.
	vms map[string]*VM
}

// NewManager creates a new VM manager.
func NewManager() *Manager {
	return &Manager{
		vms: make(map[string]*VM),
	}
}

// Create boots a new Cloud Hypervisor microVM with the given configuration.
// The network namespace and TAP device must already be set up.
func (m *Manager) Create(ctx context.Context, cfg VMConfig) (*VM, error) {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	os.Remove(cfg.SocketPath)

	slog.Info("creating VM",
		"sandbox", cfg.SandboxID,
		"vcpus", cfg.VCPUs,
		"memory_mb", cfg.MemoryMB,
	)

	// Step 1: Launch the Cloud Hypervisor process.
	proc, err := startProcess(&cfg)
	if err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}

	// Step 2: Wait for the API socket to appear.
	if err := waitForSocket(ctx, cfg.SocketPath, proc); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("wait for socket: %w", err)
	}

	// Step 3: Configure and boot the VM via a single API call.
	client := newCHClient(cfg.SocketPath)

	if err := client.createVM(ctx, &cfg); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("create VM config: %w", err)
	}

	// Step 4: Boot the VM.
	if err := client.bootVM(ctx); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("boot VM: %w", err)
	}

	vm := &VM{
		Config:  cfg,
		process: proc,
		client:  client,
	}

	m.mu.Lock()
	m.vms[cfg.SandboxID] = vm
	m.mu.Unlock()

	slog.Info("VM started successfully", "sandbox", cfg.SandboxID)

	return vm, nil
}

// Pause pauses a running VM.
func (m *Manager) Pause(ctx context.Context, sandboxID string) error {
	m.mu.RLock()
	vm, ok := m.vms[sandboxID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("VM not found: %s", sandboxID)
	}

	if err := vm.client.pauseVM(ctx); err != nil {
		return fmt.Errorf("pause VM: %w", err)
	}

	slog.Info("VM paused", "sandbox", sandboxID)
	return nil
}

// Resume resumes a paused VM.
func (m *Manager) Resume(ctx context.Context, sandboxID string) error {
	m.mu.RLock()
	vm, ok := m.vms[sandboxID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("VM not found: %s", sandboxID)
	}

	if err := vm.client.resumeVM(ctx); err != nil {
		return fmt.Errorf("resume VM: %w", err)
	}

	slog.Info("VM resumed", "sandbox", sandboxID)
	return nil
}

// UpdateBalloon adjusts the balloon target for a running VM.
// amountMiB is memory to take FROM the guest (0 = give all back).
func (m *Manager) UpdateBalloon(ctx context.Context, sandboxID string, amountMiB int) error {
	m.mu.RLock()
	vm, ok := m.vms[sandboxID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("VM not found: %s", sandboxID)
	}

	sizeBytes := int64(amountMiB) * 1024 * 1024
	return vm.client.resizeBalloon(ctx, sizeBytes)
}

// Destroy stops and cleans up a VM.
func (m *Manager) Destroy(ctx context.Context, sandboxID string) error {
	m.mu.Lock()
	vm, ok := m.vms[sandboxID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("VM not found: %s", sandboxID)
	}
	m.mu.Unlock()

	slog.Info("destroying VM", "sandbox", sandboxID)

	// Try clean shutdown first, fall back to process kill.
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 5*time.Second)
	if err := vm.client.shutdownVMM(shutdownCtx); err != nil {
		slog.Debug("clean VMM shutdown failed, killing process", "sandbox", sandboxID, "error", err)
	}
	shutdownCancel()

	if err := vm.process.stop(); err != nil {
		slog.Warn("error stopping process", "sandbox", sandboxID, "error", err)
	}

	os.Remove(vm.Config.SocketPath)

	m.mu.Lock()
	delete(m.vms, sandboxID)
	m.mu.Unlock()

	slog.Info("VM destroyed", "sandbox", sandboxID)
	return nil
}

// Snapshot creates a VM snapshot. The VM must already be paused.
// destURL is the file:// URL to the snapshot directory.
func (m *Manager) Snapshot(ctx context.Context, sandboxID, snapshotDir string) error {
	m.mu.RLock()
	vm, ok := m.vms[sandboxID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("VM not found: %s", sandboxID)
	}

	destURL := "file://" + snapshotDir
	if err := vm.client.snapshotVM(ctx, destURL); err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}

	slog.Info("VM snapshot created", "sandbox", sandboxID, "snapshot_dir", snapshotDir)
	return nil
}

// CreateFromSnapshot boots a new Cloud Hypervisor VM by restoring from a
// snapshot directory. The network namespace and TAP device must already be set up.
//
// A bare CH process is started first, then the restore is performed via the API
// with memory_restore_mode=OnDemand for UFFD-based lazy page loading. This means
// only pages the guest actually touches are faulted in from disk — a 16GB template
// with 2GB active working set only loads ~2GB into RAM at restore time.
//
// The restore API also sets resume=true, so the VM starts running immediately
// without a separate resume call.
//
// The rootfs path recorded in the snapshot is resolved via a stable symlink at
// SandboxDir/rootfs.ext4 inside the mount namespace.
//
// The sequence is:
//  1. Start bare CH process in mount+network namespace
//  2. Wait for API socket
//  3. Restore VM via API (OnDemand memory + auto-resume)
func (m *Manager) CreateFromSnapshot(ctx context.Context, cfg VMConfig, snapshotDir string) (*VM, error) {
	cfg.SnapshotDir = snapshotDir
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	os.Remove(cfg.SocketPath)

	slog.Info("restoring VM from snapshot",
		"sandbox", cfg.SandboxID,
		"snapshot_dir", snapshotDir,
	)

	// Step 1: Launch bare CH process (no --restore).
	proc, err := startProcessForRestore(&cfg)
	if err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}

	// Step 2: Wait for the API socket.
	if err := waitForSocket(ctx, cfg.SocketPath, proc); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("wait for socket: %w", err)
	}

	client := newCHClient(cfg.SocketPath)

	// Step 3: Restore via API with OnDemand memory + auto-resume.
	sourceURL := "file://" + snapshotDir
	if err := client.restoreVM(ctx, sourceURL); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("restore VM: %w", err)
	}

	vm := &VM{
		Config:  cfg,
		process: proc,
		client:  client,
	}

	m.mu.Lock()
	m.vms[cfg.SandboxID] = vm
	m.mu.Unlock()

	slog.Info("VM restored from snapshot", "sandbox", cfg.SandboxID)
	return vm, nil
}

// PID returns the process ID of the unshare wrapper process.
func (v *VM) PID() int {
	return v.process.cmd.Process.Pid
}

// Exited returns a channel that is closed when the VM process exits.
func (v *VM) Exited() <-chan struct{} {
	return v.process.exited()
}

// Get returns a running VM by sandbox ID.
func (m *Manager) Get(sandboxID string) (*VM, bool) {
	m.mu.RLock()
	vm, ok := m.vms[sandboxID]
	m.mu.RUnlock()
	return vm, ok
}

// waitForSocket polls for the Cloud Hypervisor API socket to appear on disk.
func waitForSocket(ctx context.Context, socketPath string, proc *process) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(5 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-proc.exited():
			return fmt.Errorf("cloud-hypervisor process exited before socket was ready")
		case <-timeout:
			return fmt.Errorf("timed out waiting for API socket at %s", socketPath)
		case <-ticker.C:
			if _, err := os.Stat(socketPath); err == nil {
				return nil
			}
		}
	}
}
