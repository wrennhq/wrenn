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
		proc.stop()
		return nil, fmt.Errorf("wait for socket: %w", err)
	}

	// Step 3: Configure the VM via the Firecracker API.
	client := newFCClient(cfg.SocketPath)

	if err := configureVM(ctx, client, &cfg); err != nil {
		proc.stop()
		return nil, fmt.Errorf("configure VM: %w", err)
	}

	// Step 4: Start the VM.
	if err := client.startVM(ctx); err != nil {
		proc.stop()
		return nil, fmt.Errorf("start VM: %w", err)
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

	// Root drive
	if err := client.setRootfsDrive(ctx, "rootfs", cfg.RootfsPath, false); err != nil {
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
