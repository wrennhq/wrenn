package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"git.omukk.dev/wrenn/sandbox/internal/envdclient"
	"git.omukk.dev/wrenn/sandbox/internal/filesystem"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/models"
	"git.omukk.dev/wrenn/sandbox/internal/network"
	"git.omukk.dev/wrenn/sandbox/internal/snapshot"
	"git.omukk.dev/wrenn/sandbox/internal/uffd"
	"git.omukk.dev/wrenn/sandbox/internal/vm"
)

// Config holds the paths and defaults for the sandbox manager.
type Config struct {
	KernelPath   string
	ImagesDir    string // directory containing template images (e.g., /var/lib/wrenn/images/{name}/rootfs.ext4)
	SandboxesDir string // directory for per-sandbox rootfs clones (e.g., /var/lib/wrenn/sandboxes)
	SnapshotsDir string // directory for pause snapshots (e.g., /var/lib/wrenn/snapshots/{sandbox-id}/)
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
	slot           *network.Slot
	client         *envdclient.Client
	uffdSocketPath string // non-empty for sandboxes restored from snapshot
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

	// Check if template refers to a snapshot (has snapfile + memfile + header + rootfs).
	if snapshot.IsSnapshot(m.cfg.ImagesDir, template) {
		return m.createFromSnapshot(ctx, sandboxID, template, vcpus, memoryMB, timeoutSec)
	}

	// Resolve base rootfs image: /var/lib/wrenn/images/{template}/rootfs.ext4
	baseRootfs := filepath.Join(m.cfg.ImagesDir, template, "rootfs.ext4")
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

// Destroy stops and cleans up a sandbox. If the sandbox is running, its VM,
// network, and rootfs are torn down. Any pause snapshot files are also removed.
func (m *Manager) Destroy(ctx context.Context, sandboxID string) error {
	m.mu.Lock()
	sb, ok := m.boxes[sandboxID]
	if ok {
		delete(m.boxes, sandboxID)
	}
	m.mu.Unlock()

	if ok {
		m.cleanup(ctx, sb)
	}

	// Always clean up pause snapshot files (may exist if sandbox was paused).
	snapshot.Remove(m.cfg.SnapshotsDir, sandboxID)

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
	if sb.uffdSocketPath != "" {
		os.Remove(sb.uffdSocketPath)
	}
}

// Pause takes a snapshot of a running sandbox, then destroys all resources.
// The sandbox's snapshot files are stored at SnapshotsDir/{sandboxID}/.
// After this call, the sandbox is no longer running but can be resumed.
func (m *Manager) Pause(ctx context.Context, sandboxID string) error {
	sb, err := m.get(sandboxID)
	if err != nil {
		return err
	}

	if sb.Status != models.StatusRunning {
		return fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	// Step 1: Pause the VM (freeze vCPUs).
	if err := m.vm.Pause(ctx, sandboxID); err != nil {
		return fmt.Errorf("pause VM: %w", err)
	}

	// Step 2: Take a full snapshot (snapfile + memfile).
	if err := snapshot.EnsureDir(m.cfg.SnapshotsDir, sandboxID); err != nil {
		return fmt.Errorf("create snapshot dir: %w", err)
	}

	snapDir := snapshot.DirPath(m.cfg.SnapshotsDir, sandboxID)
	rawMemPath := filepath.Join(snapDir, "memfile.raw")
	snapPath := snapshot.SnapPath(m.cfg.SnapshotsDir, sandboxID)

	if err := m.vm.Snapshot(ctx, sandboxID, snapPath, rawMemPath); err != nil {
		snapshot.Remove(m.cfg.SnapshotsDir, sandboxID)
		return fmt.Errorf("create VM snapshot: %w", err)
	}

	// Step 3: Process the raw memfile into a compact diff + header.
	buildID := uuid.New()
	diffPath := snapshot.MemDiffPath(m.cfg.SnapshotsDir, sandboxID)
	headerPath := snapshot.MemHeaderPath(m.cfg.SnapshotsDir, sandboxID)

	if _, err := snapshot.ProcessMemfile(rawMemPath, diffPath, headerPath, buildID); err != nil {
		snapshot.Remove(m.cfg.SnapshotsDir, sandboxID)
		return fmt.Errorf("process memfile: %w", err)
	}

	// Remove the raw memfile — we only keep the compact diff.
	os.Remove(rawMemPath)

	// Step 4: Copy rootfs into snapshot dir.
	snapshotRootfs := snapshot.RootfsPath(m.cfg.SnapshotsDir, sandboxID)
	if err := filesystem.CloneRootfs(sb.RootfsPath, snapshotRootfs); err != nil {
		snapshot.Remove(m.cfg.SnapshotsDir, sandboxID)
		return fmt.Errorf("copy rootfs: %w", err)
	}

	// Step 5: Destroy the sandbox (free VM, network, rootfs clone).
	m.mu.Lock()
	delete(m.boxes, sandboxID)
	m.mu.Unlock()

	m.cleanup(ctx, sb)

	slog.Info("sandbox paused (snapshot + destroy)", "id", sandboxID)
	return nil
}

// Resume restores a paused sandbox from its snapshot using UFFD for
// lazy memory loading. The sandbox gets a new network slot.
func (m *Manager) Resume(ctx context.Context, sandboxID string) (*models.Sandbox, error) {
	snapDir := m.cfg.SnapshotsDir
	if !snapshot.Exists(snapDir, sandboxID) {
		return nil, fmt.Errorf("no snapshot found for sandbox %s", sandboxID)
	}

	// Read the header to set up the UFFD memory source.
	headerData, err := os.ReadFile(snapshot.MemHeaderPath(snapDir, sandboxID))
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	header, err := snapshot.Deserialize(headerData)
	if err != nil {
		return nil, fmt.Errorf("deserialize header: %w", err)
	}

	// Build diff file map (build ID → file path).
	diffPaths := map[string]string{
		header.Metadata.BuildID.String(): snapshot.MemDiffPath(snapDir, sandboxID),
	}

	source, err := uffd.NewDiffFileSource(header, diffPaths)
	if err != nil {
		return nil, fmt.Errorf("create memory source: %w", err)
	}

	// Clone snapshot rootfs for this sandbox.
	snapshotRootfs := snapshot.RootfsPath(snapDir, sandboxID)
	rootfsPath := filepath.Join(m.cfg.SandboxesDir, fmt.Sprintf("%s-resume.ext4", sandboxID))
	if err := filesystem.CloneRootfs(snapshotRootfs, rootfsPath); err != nil {
		source.Close()
		return nil, fmt.Errorf("clone snapshot rootfs: %w", err)
	}

	// Allocate network slot.
	slotIdx, err := m.slots.Allocate()
	if err != nil {
		source.Close()
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("allocate network slot: %w", err)
	}
	slot := network.NewSlot(slotIdx)

	if err := network.CreateNetwork(slot); err != nil {
		source.Close()
		m.slots.Release(slotIdx)
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("create network: %w", err)
	}

	// Start UFFD server.
	uffdSocketPath := filepath.Join(m.cfg.SandboxesDir, fmt.Sprintf("%s-uffd.sock", sandboxID))
	os.Remove(uffdSocketPath) // Clean stale socket.
	uffdServer := uffd.NewServer(uffdSocketPath, source)
	if err := uffdServer.Start(ctx); err != nil {
		source.Close()
		network.RemoveNetwork(slot)
		m.slots.Release(slotIdx)
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("start uffd server: %w", err)
	}

	// Restore VM from snapshot.
	vmCfg := vm.VMConfig{
		SandboxID:        sandboxID,
		KernelPath:       m.cfg.KernelPath,
		RootfsPath:       rootfsPath,
		VCPUs:            int(header.Metadata.Size / (1024 * 1024)), // Will be overridden by snapshot.
		MemoryMB:         int(header.Metadata.Size / (1024 * 1024)),
		NetworkNamespace: slot.NamespaceID,
		TapDevice:        slot.TapName,
		TapMAC:           slot.TapMAC,
		GuestIP:          slot.GuestIP,
		GatewayIP:        slot.TapIP,
		NetMask:          slot.GuestNetMask,
	}

	snapPath := snapshot.SnapPath(snapDir, sandboxID)
	if _, err := m.vm.CreateFromSnapshot(ctx, vmCfg, snapPath, uffdSocketPath); err != nil {
		uffdServer.Stop()
		source.Close()
		network.RemoveNetwork(slot)
		m.slots.Release(slotIdx)
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("restore VM from snapshot: %w", err)
	}

	// Wait for envd to be ready.
	client := envdclient.New(slot.HostIP.String())
	waitCtx, waitCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
	defer waitCancel()

	if err := client.WaitUntilReady(waitCtx); err != nil {
		uffdServer.Stop()
		source.Close()
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
			Template:     "",
			VCPUs:        vmCfg.VCPUs,
			MemoryMB:     vmCfg.MemoryMB,
			TimeoutSec:   0,
			SlotIndex:    slotIdx,
			HostIP:       slot.HostIP,
			RootfsPath:   rootfsPath,
			CreatedAt:    now,
			LastActiveAt: now,
		},
		slot:           slot,
		client:         client,
		uffdSocketPath: uffdSocketPath,
	}

	m.mu.Lock()
	m.boxes[sandboxID] = sb
	m.mu.Unlock()

	// Clean up the snapshot files now that the sandbox is running.
	snapshot.Remove(snapDir, sandboxID)

	slog.Info("sandbox resumed from snapshot",
		"id", sandboxID,
		"host_ip", slot.HostIP.String(),
	)

	return &sb.Sandbox, nil
}

// CreateSnapshot creates a reusable template from a sandbox. Works on both
// running and paused sandboxes. If the sandbox is running, it is paused first.
// The sandbox remains paused after this call (it can still be resumed).
// The template files are copied to ImagesDir/{name}/.
func (m *Manager) CreateSnapshot(ctx context.Context, sandboxID, name string) (int64, error) {
	// If the sandbox is running, pause it first.
	if _, err := m.get(sandboxID); err == nil {
		if err := m.Pause(ctx, sandboxID); err != nil {
			return 0, fmt.Errorf("pause sandbox: %w", err)
		}
	}

	// At this point, pause snapshot files must exist in SnapshotsDir/{sandboxID}/.
	if !snapshot.Exists(m.cfg.SnapshotsDir, sandboxID) {
		return 0, fmt.Errorf("no snapshot found for sandbox %s", sandboxID)
	}

	// Copy snapshot files to ImagesDir/{name}/ as a reusable template.
	if err := snapshot.EnsureDir(m.cfg.ImagesDir, name); err != nil {
		return 0, fmt.Errorf("create template dir: %w", err)
	}

	srcDir := snapshot.DirPath(m.cfg.SnapshotsDir, sandboxID)
	dstDir := snapshot.DirPath(m.cfg.ImagesDir, name)

	for _, fname := range []string{snapshot.SnapFileName, snapshot.MemDiffName, snapshot.MemHeaderName, snapshot.RootfsFileName} {
		src := filepath.Join(srcDir, fname)
		dst := filepath.Join(dstDir, fname)
		if err := filesystem.CloneRootfs(src, dst); err != nil {
			snapshot.Remove(m.cfg.ImagesDir, name)
			return 0, fmt.Errorf("copy %s: %w", fname, err)
		}
	}

	sizeBytes, err := snapshot.DirSize(m.cfg.ImagesDir, name)
	if err != nil {
		slog.Warn("failed to calculate snapshot size", "error", err)
	}

	slog.Info("snapshot created",
		"sandbox", sandboxID,
		"name", name,
		"size_bytes", sizeBytes,
	)
	return sizeBytes, nil
}

// DeleteSnapshot removes a snapshot template from disk.
func (m *Manager) DeleteSnapshot(name string) error {
	return snapshot.Remove(m.cfg.ImagesDir, name)
}

// createFromSnapshot creates a new sandbox by restoring from a snapshot template
// in ImagesDir/{snapshotName}/. Uses UFFD for lazy memory loading.
func (m *Manager) createFromSnapshot(ctx context.Context, sandboxID, snapshotName string, vcpus, memoryMB, timeoutSec int) (*models.Sandbox, error) {
	imagesDir := m.cfg.ImagesDir

	// Read the header.
	headerData, err := os.ReadFile(snapshot.MemHeaderPath(imagesDir, snapshotName))
	if err != nil {
		return nil, fmt.Errorf("read snapshot header: %w", err)
	}

	header, err := snapshot.Deserialize(headerData)
	if err != nil {
		return nil, fmt.Errorf("deserialize header: %w", err)
	}

	// Snapshot determines memory size. VCPUs are also baked into the
	// snapshot state — the caller should pass the correct value from
	// the template DB record.
	memoryMB = int(header.Metadata.Size / (1024 * 1024))

	// Build diff file map.
	diffPaths := map[string]string{
		header.Metadata.BuildID.String(): snapshot.MemDiffPath(imagesDir, snapshotName),
	}

	source, err := uffd.NewDiffFileSource(header, diffPaths)
	if err != nil {
		return nil, fmt.Errorf("create memory source: %w", err)
	}

	// Clone snapshot rootfs.
	snapshotRootfs := snapshot.RootfsPath(imagesDir, snapshotName)
	rootfsPath := filepath.Join(m.cfg.SandboxesDir, fmt.Sprintf("%s-%s.ext4", sandboxID, snapshotName))
	if err := filesystem.CloneRootfs(snapshotRootfs, rootfsPath); err != nil {
		source.Close()
		return nil, fmt.Errorf("clone snapshot rootfs: %w", err)
	}

	// Allocate network.
	slotIdx, err := m.slots.Allocate()
	if err != nil {
		source.Close()
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("allocate network slot: %w", err)
	}
	slot := network.NewSlot(slotIdx)

	if err := network.CreateNetwork(slot); err != nil {
		source.Close()
		m.slots.Release(slotIdx)
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("create network: %w", err)
	}

	// Start UFFD server.
	uffdSocketPath := filepath.Join(m.cfg.SandboxesDir, fmt.Sprintf("%s-uffd.sock", sandboxID))
	os.Remove(uffdSocketPath)
	uffdServer := uffd.NewServer(uffdSocketPath, source)
	if err := uffdServer.Start(ctx); err != nil {
		source.Close()
		network.RemoveNetwork(slot)
		m.slots.Release(slotIdx)
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("start uffd server: %w", err)
	}

	// Restore VM.
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

	snapPath := snapshot.SnapPath(imagesDir, snapshotName)
	if _, err := m.vm.CreateFromSnapshot(ctx, vmCfg, snapPath, uffdSocketPath); err != nil {
		uffdServer.Stop()
		source.Close()
		network.RemoveNetwork(slot)
		m.slots.Release(slotIdx)
		os.Remove(rootfsPath)
		return nil, fmt.Errorf("restore VM from snapshot: %w", err)
	}

	// Wait for envd.
	client := envdclient.New(slot.HostIP.String())
	waitCtx, waitCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
	defer waitCancel()

	if err := client.WaitUntilReady(waitCtx); err != nil {
		uffdServer.Stop()
		source.Close()
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
			Template:     snapshotName,
			VCPUs:        vcpus,
			MemoryMB:     memoryMB,
			TimeoutSec:   timeoutSec,
			SlotIndex:    slotIdx,
			HostIP:       slot.HostIP,
			RootfsPath:   rootfsPath,
			CreatedAt:    now,
			LastActiveAt: now,
		},
		slot:           slot,
		client:         client,
		uffdSocketPath: uffdSocketPath,
	}

	m.mu.Lock()
	m.boxes[sandboxID] = sb
	m.mu.Unlock()

	slog.Info("sandbox created from snapshot",
		"id", sandboxID,
		"snapshot", snapshotName,
		"host_ip", slot.HostIP.String(),
	)

	return &sb.Sandbox, nil
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
