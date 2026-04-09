package vm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// VM represents a running Firecracker microVM.
type VM struct {
	Config  VMConfig
	process *process
	client  *fcClient
}

// Manager handles the lifecycle of Firecracker microVMs.
type Manager struct {
	// vms tracks running VMs by sandbox ID.
	vms map[string]*VM
}

// NewManager creates a new VM manager.
func NewManager() *Manager {
	return &Manager{
		vms: make(map[string]*VM),
	}
}

// Create boots a new Firecracker microVM with the given configuration.
// The network namespace and TAP device must already be set up.
func (m *Manager) Create(ctx context.Context, cfg VMConfig) (*VM, error) {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Clean up any leftover socket from a previous run.
	os.Remove(cfg.SocketPath)

	slog.Info("creating VM",
		"sandbox", cfg.SandboxID,
		"vcpus", cfg.VCPUs,
		"memory_mb", cfg.MemoryMB,
	)

	// Step 1: Launch the Firecracker process.
	proc, err := startProcess(ctx, &cfg)
	if err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}

	// Step 2: Wait for the API socket to appear.
	if err := waitForSocket(ctx, cfg.SocketPath, proc); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("wait for socket: %w", err)
	}

	// Step 3: Configure the VM via the Firecracker API.
	client := newFCClient(cfg.SocketPath)

	if err := configureVM(ctx, client, &cfg); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("configure VM: %w", err)
	}

	// Step 4: Start the VM.
	if err := client.startVM(ctx); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("start VM: %w", err)
	}

	// Step 5: Push sandbox metadata into MMDS so envd can read
	// WRENN_SANDBOX_ID and WRENN_TEMPLATE_ID from inside the guest.
	if err := client.setMMDS(ctx, cfg.SandboxID, cfg.TemplateID); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("set MMDS metadata: %w", err)
	}

	vm := &VM{
		Config:  cfg,
		process: proc,
		client:  client,
	}

	m.vms[cfg.SandboxID] = vm

	slog.Info("VM started successfully", "sandbox", cfg.SandboxID)

	return vm, nil
}

// configureVM sends the configuration to Firecracker via its HTTP API.
func configureVM(ctx context.Context, client *fcClient, cfg *VMConfig) error {
	// Boot source (kernel + args)
	if err := client.setBootSource(ctx, cfg.KernelPath, cfg.kernelArgs()); err != nil {
		return fmt.Errorf("set boot source: %w", err)
	}

	// Root drive — use the symlink path inside the mount namespace so that
	// snapshots record a stable path that works on restore.
	rootfsSymlink := cfg.SandboxDir + "/rootfs.ext4"
	if err := client.setRootfsDrive(ctx, "rootfs", rootfsSymlink, false); err != nil {
		return fmt.Errorf("set rootfs drive: %w", err)
	}

	// Network interface
	if err := client.setNetworkInterface(ctx, "eth0", cfg.TapDevice, cfg.TapMAC); err != nil {
		return fmt.Errorf("set network interface: %w", err)
	}

	// Machine config (vCPUs + memory)
	if err := client.setMachineConfig(ctx, cfg.VCPUs, cfg.MemoryMB); err != nil {
		return fmt.Errorf("set machine config: %w", err)
	}

	// MMDS config — enable V2 token access on eth0 so that envd can read
	// WRENN_SANDBOX_ID and WRENN_TEMPLATE_ID from inside the guest.
	if err := client.setMMDSConfig(ctx, "eth0"); err != nil {
		return fmt.Errorf("set MMDS config: %w", err)
	}

	return nil
}

// Pause pauses a running VM.
func (m *Manager) Pause(ctx context.Context, sandboxID string) error {
	vm, ok := m.vms[sandboxID]
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
	vm, ok := m.vms[sandboxID]
	if !ok {
		return fmt.Errorf("VM not found: %s", sandboxID)
	}

	if err := vm.client.resumeVM(ctx); err != nil {
		return fmt.Errorf("resume VM: %w", err)
	}

	slog.Info("VM resumed", "sandbox", sandboxID)
	return nil
}

// Destroy stops and cleans up a VM.
func (m *Manager) Destroy(ctx context.Context, sandboxID string) error {
	vm, ok := m.vms[sandboxID]
	if !ok {
		return fmt.Errorf("VM not found: %s", sandboxID)
	}

	slog.Info("destroying VM", "sandbox", sandboxID)

	// Stop the Firecracker process.
	if err := vm.process.stop(); err != nil {
		slog.Warn("error stopping process", "sandbox", sandboxID, "error", err)
	}

	// Clean up the API socket.
	os.Remove(vm.Config.SocketPath)

	delete(m.vms, sandboxID)

	slog.Info("VM destroyed", "sandbox", sandboxID)
	return nil
}

// Snapshot creates a VM snapshot. The VM must already be paused.
// snapshotType is "Full" (all memory) or "Diff" (only dirty pages since last resume).
func (m *Manager) Snapshot(ctx context.Context, sandboxID, snapPath, memPath, snapshotType string) error {
	vm, ok := m.vms[sandboxID]
	if !ok {
		return fmt.Errorf("VM not found: %s", sandboxID)
	}

	if err := vm.client.createSnapshot(ctx, snapPath, memPath, snapshotType); err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}

	slog.Info("VM snapshot created", "sandbox", sandboxID, "snap_path", snapPath, "type", snapshotType)
	return nil
}

// CreateFromSnapshot boots a new Firecracker VM by loading a snapshot
// using UFFD for lazy memory loading. The network namespace and TAP
// device must already be set up.
//
// No boot resources (kernel, drives, machine config) are configured —
// the snapshot carries all that state. The rootfs path recorded in the
// snapshot is resolved via a stable symlink at SandboxDir/rootfs.ext4
// inside the mount namespace (created by the start script in jailer.go).
//
// The sequence is:
//  1. Start FC process in mount+network namespace (creates tmpfs + rootfs symlink)
//  2. Wait for API socket
//  3. Load snapshot with UFFD backend
//  4. Resume VM execution
func (m *Manager) CreateFromSnapshot(ctx context.Context, cfg VMConfig, snapPath, uffdSocketPath string) (*VM, error) {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	os.Remove(cfg.SocketPath)

	slog.Info("restoring VM from snapshot",
		"sandbox", cfg.SandboxID,
		"snap_path", snapPath,
	)

	// Step 1: Launch the Firecracker process.
	// The start script creates a tmpfs at SandboxDir and symlinks
	// rootfs.ext4 → cfg.RootfsPath, so the snapshot's recorded rootfs
	// path (/fc-vm/rootfs.ext4) resolves to the new clone.
	proc, err := startProcess(ctx, &cfg)
	if err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}

	// Step 2: Wait for the API socket.
	if err := waitForSocket(ctx, cfg.SocketPath, proc); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("wait for socket: %w", err)
	}

	client := newFCClient(cfg.SocketPath)

	// Step 3: Load the snapshot with UFFD backend.
	// No boot resources are configured — the snapshot carries kernel,
	// drive, network, and machine config state.
	if err := client.loadSnapshotWithUffd(ctx, snapPath, uffdSocketPath); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("load snapshot: %w", err)
	}

	// Step 4: Resume the VM.
	if err := client.resumeVM(ctx); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("resume VM: %w", err)
	}

	// Step 5: Push sandbox metadata into MMDS.
	if err := client.setMMDS(ctx, cfg.SandboxID, cfg.TemplateID); err != nil {
		_ = proc.stop()
		return nil, fmt.Errorf("set MMDS metadata: %w", err)
	}

	vm := &VM{
		Config:  cfg,
		process: proc,
		client:  client,
	}

	m.vms[cfg.SandboxID] = vm

	slog.Info("VM restored from snapshot", "sandbox", cfg.SandboxID)
	return vm, nil
}

// PID returns the process ID of the unshare wrapper process.
// The actual Firecracker process is a direct child of this PID.
func (v *VM) PID() int {
	return v.process.cmd.Process.Pid
}

// Get returns a running VM by sandbox ID.
func (m *Manager) Get(sandboxID string) (*VM, bool) {
	vm, ok := m.vms[sandboxID]
	return vm, ok
}

// waitForSocket polls for the Firecracker API socket to appear on disk.
func waitForSocket(ctx context.Context, socketPath string, proc *process) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(5 * time.Second)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-proc.exited():
			return fmt.Errorf("firecracker process exited before socket was ready")
		case <-timeout:
			return fmt.Errorf("timed out waiting for API socket at %s", socketPath)
		case <-ticker.C:
			if _, err := os.Stat(socketPath); err == nil {
				return nil
			}
		}
	}
}
