package vm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
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

// Pause freezes a running VM's vCPUs via the CH API.
func (m *Manager) Pause(ctx context.Context, sandboxID string) error {
	vm, ok := m.Get(sandboxID)
	if !ok {
		return fmt.Errorf("VM not found: %s", sandboxID)
	}
	return vm.client.pauseVM(ctx)
}

// Resume unfreezes a paused VM via the CH API.
func (m *Manager) Resume(ctx context.Context, sandboxID string) error {
	vm, ok := m.Get(sandboxID)
	if !ok {
		return fmt.Errorf("VM not found: %s", sandboxID)
	}
	return vm.client.resumeVM(ctx)
}

// Info returns the CH VM state (e.g. "Running", "Paused", "Shutdown") via
// the CH unix-socket API. Returns an error if the socket is dead or the VM
// is not registered. Use to probe liveness before issuing destructive ops
// like pause or snapshot.
func (m *Manager) Info(ctx context.Context, sandboxID string) (string, error) {
	vm, ok := m.Get(sandboxID)
	if !ok {
		return "", fmt.Errorf("VM not found: %s", sandboxID)
	}
	return vm.client.vmInfo(ctx)
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

// Snapshot writes the VM's config/state/memory to snapshotDir via CH's
// vm.snapshot API. The VM must already be paused. snapshotDir must be an
// absolute path; it is passed to CH as `file://{dir}/`.
func (m *Manager) Snapshot(ctx context.Context, sandboxID, snapshotDir string) error {
	vm, ok := m.Get(sandboxID)
	if !ok {
		return fmt.Errorf("VM not found: %s", sandboxID)
	}
	if err := os.MkdirAll(snapshotDir, 0o755); err != nil {
		return fmt.Errorf("mkdir snapshot dir: %w", err)
	}
	url := "file://" + strings.TrimRight(snapshotDir, "/") + "/"
	if err := vm.client.snapshotVM(ctx, url); err != nil {
		return fmt.Errorf("vm.snapshot: %w", err)
	}
	slog.Info("VM snapshot written", "sandbox", sandboxID, "dir", snapshotDir)
	return nil
}

// CreateFromSnapshot launches a Cloud Hypervisor process in restore mode,
// connecting it to an existing snapshot directory. The VM is left in the
// paused state — the caller is expected to call Resume after any post-restore
// setup (e.g. re-acquiring envd connectivity is implicit via TCP).
//
// cfg.RestoreFromDir must point to an absolute path containing the CH
// snapshot artefacts. The disk path inside config.json must already resolve
// (CH receives the same SandboxDir/rootfs.ext4 symlink as for fresh boot).
func (m *Manager) CreateFromSnapshot(ctx context.Context, cfg VMConfig) (*VM, error) {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if cfg.RestoreFromDir == "" {
		return nil, fmt.Errorf("RestoreFromDir is required for restore")
	}

	os.Remove(cfg.SocketPath)

	slog.Info("restoring VM from snapshot",
		"sandbox", cfg.SandboxID,
		"restore_dir", cfg.RestoreFromDir,
		"lazy_memory", cfg.RestoreLazyMemory,
	)

	proc, err := startRestoreProcess(&cfg)
	if err != nil {
		return nil, fmt.Errorf("start restore process: %w", err)
	}

	if err := waitForSocket(ctx, cfg.SocketPath, proc); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("wait for socket: %w", err)
	}

	client := newCHClient(cfg.SocketPath)

	// Confirm CH actually hydrated the snapshot before registering. Without
	// this check, a broken snapshot would leave a zombie *VM in the map that
	// blocks future restores for the same sandbox ID.
	state, err := client.vmInfo(ctx)
	if err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("vm.info after restore: %w", err)
	}
	if state != "Paused" {
		_ = proc.stop()
		return nil, fmt.Errorf("unexpected post-restore VM state %q (want Paused)", state)
	}

	vm := &VM{
		Config:  cfg,
		process: proc,
		client:  client,
	}

	m.mu.Lock()
	m.vms[cfg.SandboxID] = vm
	m.mu.Unlock()

	slog.Info("VM restored from snapshot (paused)", "sandbox", cfg.SandboxID)
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
