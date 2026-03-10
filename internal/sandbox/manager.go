package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"git.omukk.dev/wrenn/sandbox/internal/envdclient"
	"git.omukk.dev/wrenn/sandbox/internal/filesystem"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/models"
	"git.omukk.dev/wrenn/sandbox/internal/network"
	"git.omukk.dev/wrenn/sandbox/internal/vm"
)

// Config holds the paths and defaults for the sandbox manager.
type Config struct {
	KernelPath   string
	ImagesDir    string // directory containing base rootfs images (e.g., /var/lib/wrenn/images/minimal.ext4)
	SandboxesDir string // directory for per-sandbox rootfs clones (e.g., /var/lib/wrenn/sandboxes)
	EnvdTimeout  time.Duration
}

// Manager orchestrates sandbox lifecycle: VM, network, filesystem, envd.
type Manager struct {
	cfg    Config
	vm     *vm.Manager
	slots  *network.SlotAllocator
	mu     sync.RWMutex
	boxes  map[string]*sandboxState
	stopCh chan struct{}
}

// sandboxState holds the runtime state for a single sandbox.
type sandboxState struct {
	models.Sandbox
	slot   *network.Slot
	client *envdclient.Client
}

// New creates a new sandbox manager.
func New(cfg Config) *Manager {
	if cfg.EnvdTimeout == 0 {
		cfg.EnvdTimeout = 30 * time.Second
	}
	return &Manager{
		cfg:    cfg,
		vm:     vm.NewManager(),
		slots:  network.NewSlotAllocator(),
		boxes:  make(map[string]*sandboxState),
		stopCh: make(chan struct{}),
	}
}

// Create boots a new sandbox: clone rootfs, set up network, start VM, wait for envd.
// If sandboxID is empty, a new ID is generated.
func (m *Manager) Create(ctx context.Context, sandboxID, template string, vcpus, memoryMB, timeoutSec int) (*models.Sandbox, error) {
	if sandboxID == "" {
		sandboxID = id.NewSandboxID()
	}

	if vcpus <= 0 {
		vcpus = 1
	}
	if memoryMB <= 0 {
		memoryMB = 512
	}

	if template == "" {
		template = "minimal"
	}

	// Resolve base rootfs image: /var/lib/wrenn/images/{template}.ext4
	baseRootfs := filepath.Join(m.cfg.ImagesDir, template+".ext4")
	if _, err := os.Stat(baseRootfs); err != nil {
		return nil, fmt.Errorf("base rootfs not found at %s: %w", baseRootfs, err)
	}

	// Clone rootfs.
	rootfsPath := filepath.Join(m.cfg.SandboxesDir, fmt.Sprintf("%s-%s.ext4", sandboxID, template))
	if err := filesystem.CloneRootfs(baseRootfs, rootfsPath); err != nil {
		return nil, fmt.Errorf("clone rootfs: %w", err)
	}

	// Allocate network slot.
	slotIdx, err := m.slots.Allocate()
	if err != nil {
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("allocate network slot: %w", err)
	}
	slot := network.NewSlot(slotIdx)

	// Set up network.
	if err := network.CreateNetwork(slot); err != nil {
		m.slots.Release(slotIdx)
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("create network: %w", err)
	}

	// Boot VM.
	vmCfg := vm.VMConfig{
		SandboxID:        sandboxID,
		KernelPath:       m.cfg.KernelPath,
		RootfsPath:       rootfsPath,
		VCPUs:            vcpus,
		MemoryMB:         memoryMB,
		NetworkNamespace: slot.NamespaceID,
		TapDevice:        slot.TapName,
		TapMAC:           slot.TapMAC,
		GuestIP:          slot.GuestIP,
		GatewayIP:        slot.TapIP,
		NetMask:          slot.GuestNetMask,
	}

	if _, err := m.vm.Create(ctx, vmCfg); err != nil {
		network.RemoveNetwork(slot)
		m.slots.Release(slotIdx)
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("create VM: %w", err)
	}

	// Wait for envd to be ready.
	client := envdclient.New(slot.HostIP.String())
	waitCtx, waitCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
	defer waitCancel()

	if err := client.WaitUntilReady(waitCtx); err != nil {
		m.vm.Destroy(context.Background(), sandboxID)
		network.RemoveNetwork(slot)
		m.slots.Release(slotIdx)
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("wait for envd: %w", err)
	}

	now := time.Now()
	sb := &sandboxState{
		Sandbox: models.Sandbox{
			ID:           sandboxID,
			Status:       models.StatusRunning,
			Template:     template,
			VCPUs:        vcpus,
			MemoryMB:     memoryMB,
			TimeoutSec:   timeoutSec,
			SlotIndex:    slotIdx,
			HostIP:       slot.HostIP,
			RootfsPath:   rootfsPath,
			CreatedAt:    now,
			LastActiveAt: now,
		},
		slot:   slot,
		client: client,
	}

	m.mu.Lock()
	m.boxes[sandboxID] = sb
	m.mu.Unlock()

	slog.Info("sandbox created",
		"id", sandboxID,
		"template", template,
		"host_ip", slot.HostIP.String(),
	)

	return &sb.Sandbox, nil
}

// Destroy stops and cleans up a sandbox.
func (m *Manager) Destroy(ctx context.Context, sandboxID string) error {
	m.mu.Lock()
	sb, ok := m.boxes[sandboxID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("sandbox not found: %s", sandboxID)
	}
	delete(m.boxes, sandboxID)
	m.mu.Unlock()

	m.cleanup(ctx, sb)

	slog.Info("sandbox destroyed", "id", sandboxID)
	return nil
}

// cleanup tears down all resources for a sandbox.
func (m *Manager) cleanup(ctx context.Context, sb *sandboxState) {
	if err := m.vm.Destroy(ctx, sb.ID); err != nil {
		slog.Warn("vm destroy error", "id", sb.ID, "error", err)
	}
	if err := network.RemoveNetwork(sb.slot); err != nil {
		slog.Warn("network cleanup error", "id", sb.ID, "error", err)
	}
	m.slots.Release(sb.SlotIndex)
	os.Remove(sb.RootfsPath)
}

// Pause pauses a running sandbox.
func (m *Manager) Pause(ctx context.Context, sandboxID string) error {
	sb, err := m.get(sandboxID)
	if err != nil {
		return err
	}

	if sb.Status != models.StatusRunning {
		return fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	if err := m.vm.Pause(ctx, sandboxID); err != nil {
		return fmt.Errorf("pause VM: %w", err)
	}

	m.mu.Lock()
	sb.Status = models.StatusPaused
	m.mu.Unlock()

	slog.Info("sandbox paused", "id", sandboxID)
	return nil
}

// Resume resumes a paused sandbox.
func (m *Manager) Resume(ctx context.Context, sandboxID string) error {
	sb, err := m.get(sandboxID)
	if err != nil {
		return err
	}

	if sb.Status != models.StatusPaused {
		return fmt.Errorf("sandbox %s is not paused (status: %s)", sandboxID, sb.Status)
	}

	if err := m.vm.Resume(ctx, sandboxID); err != nil {
		return fmt.Errorf("resume VM: %w", err)
	}

	m.mu.Lock()
	sb.Status = models.StatusRunning
	sb.LastActiveAt = time.Now()
	m.mu.Unlock()

	slog.Info("sandbox resumed", "id", sandboxID)
	return nil
}

// Exec runs a command inside a sandbox.
func (m *Manager) Exec(ctx context.Context, sandboxID string, cmd string, args ...string) (*envdclient.ExecResult, error) {
	sb, err := m.get(sandboxID)
	if err != nil {
		return nil, err
	}

	if sb.Status != models.StatusRunning {
		return nil, fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	m.mu.Lock()
	sb.LastActiveAt = time.Now()
	m.mu.Unlock()

	return sb.client.Exec(ctx, cmd, args...)
}

// ExecStream runs a command inside a sandbox and returns a channel of streaming events.
func (m *Manager) ExecStream(ctx context.Context, sandboxID string, cmd string, args ...string) (<-chan envdclient.ExecStreamEvent, error) {
	sb, err := m.get(sandboxID)
	if err != nil {
		return nil, err
	}

	if sb.Status != models.StatusRunning {
		return nil, fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	m.mu.Lock()
	sb.LastActiveAt = time.Now()
	m.mu.Unlock()

	return sb.client.ExecStream(ctx, cmd, args...)
}

// List returns all sandboxes.
func (m *Manager) List() []models.Sandbox {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]models.Sandbox, 0, len(m.boxes))
	for _, sb := range m.boxes {
		result = append(result, sb.Sandbox)
	}
	return result
}

// Get returns a sandbox by ID.
func (m *Manager) Get(sandboxID string) (*models.Sandbox, error) {
	sb, err := m.get(sandboxID)
	if err != nil {
		return nil, err
	}
	return &sb.Sandbox, nil
}

// GetClient returns the envd client for a sandbox.
func (m *Manager) GetClient(sandboxID string) (*envdclient.Client, error) {
	sb, err := m.get(sandboxID)
	if err != nil {
		return nil, err
	}
	if sb.Status != models.StatusRunning {
		return nil, fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}
	return sb.client, nil
}

func (m *Manager) get(sandboxID string) (*sandboxState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sb, ok := m.boxes[sandboxID]
	if !ok {
		return nil, fmt.Errorf("sandbox not found: %s", sandboxID)
	}
	return sb, nil
}

// StartTTLReaper starts a background goroutine that destroys sandboxes
// that have exceeded their TTL (timeout_sec of inactivity).
func (m *Manager) StartTTLReaper(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.reapExpired(ctx)
			}
		}
	}()
}

func (m *Manager) reapExpired(ctx context.Context) {
	m.mu.RLock()
	var expired []string
	now := time.Now()
	for id, sb := range m.boxes {
		if sb.TimeoutSec <= 0 {
			continue
		}
		if sb.Status != models.StatusRunning && sb.Status != models.StatusPaused {
			continue
		}
		if now.Sub(sb.LastActiveAt) > time.Duration(sb.TimeoutSec)*time.Second {
			expired = append(expired, id)
		}
	}
	m.mu.RUnlock()

	for _, id := range expired {
		slog.Info("TTL expired, destroying sandbox", "id", id)
		if err := m.Destroy(ctx, id); err != nil {
			slog.Warn("TTL reap failed", "id", id, "error", err)
		}
	}
}

// Shutdown destroys all sandboxes and stops the TTL reaper.
func (m *Manager) Shutdown(ctx context.Context) {
	close(m.stopCh)

	m.mu.Lock()
	ids := make([]string, 0, len(m.boxes))
	for id := range m.boxes {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, sbID := range ids {
		slog.Info("shutdown: destroying sandbox", "id", sbID)
		if err := m.Destroy(ctx, sbID); err != nil {
			slog.Warn("shutdown destroy failed", "id", sbID, "error", err)
		}
	}
}
