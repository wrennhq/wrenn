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

	"git.omukk.dev/wrenn/sandbox/internal/devicemapper"
	"git.omukk.dev/wrenn/sandbox/internal/envdclient"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/models"
	"git.omukk.dev/wrenn/sandbox/internal/network"
	"git.omukk.dev/wrenn/sandbox/internal/snapshot"
	"git.omukk.dev/wrenn/sandbox/internal/uffd"
	"git.omukk.dev/wrenn/sandbox/internal/validate"
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
	loops  *devicemapper.LoopRegistry
	mu     sync.RWMutex
	boxes  map[string]*sandboxState
	stopCh chan struct{}

	autoPausedMu  sync.Mutex
	autoPausedIDs []string
}

// sandboxState holds the runtime state for a single sandbox.
type sandboxState struct {
	models.Sandbox
	slot           *network.Slot
	client         *envdclient.Client
	uffdSocketPath string // non-empty for sandboxes restored from snapshot
	dmDevice       *devicemapper.SnapshotDevice
	baseImagePath  string // path to the base template rootfs (for loop registry release)

	// parent holds the snapshot header and diff file paths from which this
	// sandbox was restored. Non-nil means re-pause should use "Diff" snapshot
	// type instead of "Full", avoiding the UFFD fault-in storm.
	parent *snapshotParent

	// Metrics sampling state.
	fcPID         int                // Firecracker process PID (child of unshare wrapper)
	ring          *metricsRing       // tiered ring buffers for CPU/mem/disk metrics
	samplerCancel context.CancelFunc // cancels the per-sandbox sampling goroutine
	samplerDone   chan struct{}      // closed when the sampling goroutine exits
}

// snapshotParent stores the previous generation's snapshot state so that
// re-pause can produce an incremental diff instead of a full memory dump.
type snapshotParent struct {
	header    *snapshot.Header
	diffPaths map[string]string // build ID → file path
}

// maxDiffGenerations caps how many incremental diff generations we chain
// before falling back to a Full snapshot to collapse the chain.
const maxDiffGenerations = 10

// New creates a new sandbox manager.
func New(cfg Config) *Manager {
	if cfg.EnvdTimeout == 0 {
		cfg.EnvdTimeout = 30 * time.Second
	}
	return &Manager{
		cfg:    cfg,
		vm:     vm.NewManager(),
		slots:  network.NewSlotAllocator(),
		loops:  devicemapper.NewLoopRegistry(),
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
	if err := validate.SafeName(template); err != nil {
		return nil, fmt.Errorf("invalid template name: %w", err)
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

	// Acquire shared read-only loop device for the base image.
	originLoop, err := m.loops.Acquire(baseRootfs)
	if err != nil {
		return nil, fmt.Errorf("acquire loop device: %w", err)
	}

	originSize, err := devicemapper.OriginSizeBytes(originLoop)
	if err != nil {
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("get origin size: %w", err)
	}

	// Create dm-snapshot with per-sandbox CoW file.
	dmName := "wrenn-" + sandboxID
	cowPath := filepath.Join(m.cfg.SandboxesDir, fmt.Sprintf("%s.cow", sandboxID))
	dmDev, err := devicemapper.CreateSnapshot(dmName, originLoop, cowPath, originSize)
	if err != nil {
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("create dm-snapshot: %w", err)
	}

	// Allocate network slot.
	slotIdx, err := m.slots.Allocate()
	if err != nil {
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		os.Remove(cowPath)
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("allocate network slot: %w", err)
	}
	slot := network.NewSlot(slotIdx)

	// Set up network.
	if err := network.CreateNetwork(slot); err != nil {
		m.slots.Release(slotIdx)
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		os.Remove(cowPath)
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("create network: %w", err)
	}

	// Boot VM — Firecracker gets the dm device path.
	vmCfg := vm.VMConfig{
		SandboxID:        sandboxID,
		KernelPath:       m.cfg.KernelPath,
		RootfsPath:       dmDev.DevicePath,
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
		warnErr("network cleanup error", sandboxID, network.RemoveNetwork(slot))
		m.slots.Release(slotIdx)
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		os.Remove(cowPath)
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("create VM: %w", err)
	}

	// Wait for envd to be ready.
	client := envdclient.New(slot.HostIP.String())
	waitCtx, waitCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
	defer waitCancel()

	if err := client.WaitUntilReady(waitCtx); err != nil {
		warnErr("vm destroy error", sandboxID, m.vm.Destroy(context.Background(), sandboxID))
		warnErr("network cleanup error", sandboxID, network.RemoveNetwork(slot))
		m.slots.Release(slotIdx)
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		os.Remove(cowPath)
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("wait for envd: %w", err)
	}

	// Sync guest clock in background. Non-fatal — sandbox is usable before this completes.
	// Run in a goroutine so Init latency doesn't block the RPC response back to the control plane.
	go func() {
		initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer initCancel()
		if err := client.Init(initCtx); err != nil {
			slog.Warn("envd init (clock sync) failed", "sandbox", sandboxID, "error", err)
		}
	}()

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
			RootfsPath:   dmDev.DevicePath,
			CreatedAt:    now,
			LastActiveAt: now,
		},
		slot:          slot,
		client:        client,
		dmDevice:      dmDev,
		baseImagePath: baseRootfs,
	}

	m.mu.Lock()
	m.boxes[sandboxID] = sb
	m.mu.Unlock()

	m.startSampler(sb)

	slog.Info("sandbox created",
		"id", sandboxID,
		"template", template,
		"host_ip", slot.HostIP.String(),
		"dm_device", dmDev.DevicePath,
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
	warnErr("snapshot cleanup error", sandboxID, snapshot.Remove(m.cfg.SnapshotsDir, sandboxID))

	slog.Info("sandbox destroyed", "id", sandboxID)
	return nil
}

// cleanup tears down all resources for a sandbox.
func (m *Manager) cleanup(ctx context.Context, sb *sandboxState) {
	m.stopSampler(sb)
	if err := m.vm.Destroy(ctx, sb.ID); err != nil {
		slog.Warn("vm destroy error", "id", sb.ID, "error", err)
	}
	if err := network.RemoveNetwork(sb.slot); err != nil {
		slog.Warn("network cleanup error", "id", sb.ID, "error", err)
	}
	m.slots.Release(sb.SlotIndex)

	// Tear down dm-snapshot and release the base image loop device.
	if sb.dmDevice != nil {
		if err := devicemapper.RemoveSnapshot(context.Background(), sb.dmDevice); err != nil {
			slog.Warn("dm-snapshot remove error", "id", sb.ID, "error", err)
		}
		os.Remove(sb.dmDevice.CowPath)
	}
	if sb.baseImagePath != "" {
		m.loops.Release(sb.baseImagePath)
	}

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

	pauseStart := time.Now()

	// Step 1: Pause the VM (freeze vCPUs).
	if err := m.vm.Pause(ctx, sandboxID); err != nil {
		return fmt.Errorf("pause VM: %w", err)
	}
	slog.Debug("pause: VM paused", "id", sandboxID, "elapsed", time.Since(pauseStart))

	// Determine snapshot type: Diff if resumed from snapshot (avoids UFFD
	// fault-in storm), Full otherwise or if generation cap is reached.
	snapshotType := "Full"
	if sb.parent != nil && sb.parent.header.Metadata.Generation < maxDiffGenerations {
		snapshotType = "Diff"
	}

	// resumeOnError unpauses the VM so the sandbox stays usable when a
	// post-freeze step fails. If the resume itself fails, the sandbox is
	// left frozen — the caller should destroy it.
	resumeOnError := func() {
		if err := m.vm.Resume(ctx, sandboxID); err != nil {
			slog.Error("failed to resume VM after pause error — sandbox is frozen", "id", sandboxID, "error", err)
		}
	}

	// Step 2: Take VM state snapshot (snapfile + memfile).
	if err := snapshot.EnsureDir(m.cfg.SnapshotsDir, sandboxID); err != nil {
		resumeOnError()
		return fmt.Errorf("create snapshot dir: %w", err)
	}

	snapDir := snapshot.DirPath(m.cfg.SnapshotsDir, sandboxID)
	rawMemPath := filepath.Join(snapDir, "memfile.raw")
	snapPath := snapshot.SnapPath(m.cfg.SnapshotsDir, sandboxID)

	snapshotStart := time.Now()
	if err := m.vm.Snapshot(ctx, sandboxID, snapPath, rawMemPath, snapshotType); err != nil {
		warnErr("snapshot dir cleanup error", sandboxID, snapshot.Remove(m.cfg.SnapshotsDir, sandboxID))
		resumeOnError()
		return fmt.Errorf("create VM snapshot: %w", err)
	}
	slog.Debug("pause: FC snapshot created", "id", sandboxID, "type", snapshotType, "elapsed", time.Since(snapshotStart))

	// Step 3: Process the raw memfile into a compact diff + header.
	buildID := uuid.New()
	headerPath := snapshot.MemHeaderPath(m.cfg.SnapshotsDir, sandboxID)

	processStart := time.Now()
	if sb.parent != nil && snapshotType == "Diff" {
		// Diff: process against parent header, producing only changed blocks.
		diffPath := snapshot.MemDiffPathForBuild(m.cfg.SnapshotsDir, sandboxID, buildID)
		if _, err := snapshot.ProcessMemfileWithParent(rawMemPath, diffPath, headerPath, sb.parent.header, buildID); err != nil {
			warnErr("snapshot dir cleanup error", sandboxID, snapshot.Remove(m.cfg.SnapshotsDir, sandboxID))
			resumeOnError()
			return fmt.Errorf("process memfile with parent: %w", err)
		}

		// Copy previous generation diff files into the snapshot directory.
		for prevBuildID, prevPath := range sb.parent.diffPaths {
			dstPath := snapshot.MemDiffPathForBuild(m.cfg.SnapshotsDir, sandboxID, uuid.MustParse(prevBuildID))
			if prevPath != dstPath {
				if err := copyFile(prevPath, dstPath); err != nil {
					warnErr("snapshot dir cleanup error", sandboxID, snapshot.Remove(m.cfg.SnapshotsDir, sandboxID))
					resumeOnError()
					return fmt.Errorf("copy parent diff file: %w", err)
				}
			}
		}
	} else {
		// Full: first generation or generation cap reached — single diff file.
		diffPath := snapshot.MemDiffPath(m.cfg.SnapshotsDir, sandboxID)
		if _, err := snapshot.ProcessMemfile(rawMemPath, diffPath, headerPath, buildID); err != nil {
			warnErr("snapshot dir cleanup error", sandboxID, snapshot.Remove(m.cfg.SnapshotsDir, sandboxID))
			resumeOnError()
			return fmt.Errorf("process memfile: %w", err)
		}
	}
	slog.Debug("pause: memfile processed", "id", sandboxID, "type", snapshotType, "elapsed", time.Since(processStart))

	// Remove the raw memfile — we only keep the compact diff(s).
	os.Remove(rawMemPath)

	// Step 4: Destroy the VM first so Firecracker releases the dm device.
	if err := m.vm.Destroy(ctx, sb.ID); err != nil {
		slog.Warn("vm destroy error during pause", "id", sb.ID, "error", err)
	}

	// Step 5: Now that FC is gone, safely remove the dm-snapshot and save the CoW.
	if sb.dmDevice != nil {
		if err := devicemapper.RemoveSnapshot(ctx, sb.dmDevice); err != nil {
			// Hard error: if the dm device isn't removed, the CoW file is still
			// in use and we can't safely move it. The VM is already destroyed so
			// the sandbox is unrecoverable — clean up remaining resources.
			// Note: we intentionally skip m.loops.Release here because the stale
			// dm device still references the origin loop device. Detaching it now
			// would corrupt the dm device. CleanupStaleDevices handles this on
			// next agent startup.
			warnErr("network cleanup error during pause", sandboxID, network.RemoveNetwork(sb.slot))
			m.slots.Release(sb.SlotIndex)
			if sb.uffdSocketPath != "" {
				os.Remove(sb.uffdSocketPath)
			}
			warnErr("snapshot dir cleanup error", sandboxID, snapshot.Remove(m.cfg.SnapshotsDir, sandboxID))
			m.mu.Lock()
			delete(m.boxes, sandboxID)
			m.mu.Unlock()
			return fmt.Errorf("remove dm-snapshot: %w", err)
		}

		// Move (not copy) the CoW file into the snapshot directory.
		snapshotCow := snapshot.CowPath(m.cfg.SnapshotsDir, sandboxID)
		if err := os.Rename(sb.dmDevice.CowPath, snapshotCow); err != nil {
			warnErr("snapshot dir cleanup error", sandboxID, snapshot.Remove(m.cfg.SnapshotsDir, sandboxID))
			// VM and dm-snapshot are already gone — clean up remaining resources.
			warnErr("network cleanup error during pause", sandboxID, network.RemoveNetwork(sb.slot))
			m.slots.Release(sb.SlotIndex)
			if sb.baseImagePath != "" {
				m.loops.Release(sb.baseImagePath)
			}
			if sb.uffdSocketPath != "" {
				os.Remove(sb.uffdSocketPath)
			}
			m.mu.Lock()
			delete(m.boxes, sandboxID)
			m.mu.Unlock()
			return fmt.Errorf("move cow file: %w", err)
		}

		// Record which base template this CoW was built against.
		if err := snapshot.WriteMeta(m.cfg.SnapshotsDir, sandboxID, &snapshot.RootfsMeta{
			BaseTemplate: sb.baseImagePath,
		}); err != nil {
			warnErr("snapshot dir cleanup error", sandboxID, snapshot.Remove(m.cfg.SnapshotsDir, sandboxID))
			// VM and dm-snapshot are already gone — clean up remaining resources.
			warnErr("network cleanup error during pause", sandboxID, network.RemoveNetwork(sb.slot))
			m.slots.Release(sb.SlotIndex)
			if sb.baseImagePath != "" {
				m.loops.Release(sb.baseImagePath)
			}
			if sb.uffdSocketPath != "" {
				os.Remove(sb.uffdSocketPath)
			}
			m.mu.Lock()
			delete(m.boxes, sandboxID)
			m.mu.Unlock()
			return fmt.Errorf("write rootfs meta: %w", err)
		}
	}

	// Step 6: Clean up remaining resources (network, loop device, uffd socket).
	if err := network.RemoveNetwork(sb.slot); err != nil {
		slog.Warn("network cleanup error during pause", "id", sb.ID, "error", err)
	}
	m.slots.Release(sb.SlotIndex)
	if sb.baseImagePath != "" {
		m.loops.Release(sb.baseImagePath)
	}
	if sb.uffdSocketPath != "" {
		os.Remove(sb.uffdSocketPath)
	}

	m.mu.Lock()
	delete(m.boxes, sandboxID)
	m.mu.Unlock()

	slog.Info("sandbox paused", "id", sandboxID, "snapshot_type", snapshotType, "total_elapsed", time.Since(pauseStart))
	return nil
}

// Resume restores a paused sandbox from its snapshot using UFFD for
// lazy memory loading. The sandbox gets a new network slot.
func (m *Manager) Resume(ctx context.Context, sandboxID string, timeoutSec int) (*models.Sandbox, error) {
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

	// Build diff file map — supports both single-generation and multi-generation.
	diffPaths, err := snapshot.ListDiffFiles(snapDir, sandboxID, header)
	if err != nil {
		return nil, fmt.Errorf("list diff files: %w", err)
	}

	source, err := uffd.NewDiffFileSource(header, diffPaths)
	if err != nil {
		return nil, fmt.Errorf("create memory source: %w", err)
	}

	// Read rootfs metadata to find the base template image.
	meta, err := snapshot.ReadMeta(snapDir, sandboxID)
	if err != nil {
		source.Close()
		return nil, fmt.Errorf("read rootfs meta: %w", err)
	}

	// Acquire the base image loop device and restore dm-snapshot from saved CoW.
	baseImagePath := meta.BaseTemplate
	originLoop, err := m.loops.Acquire(baseImagePath)
	if err != nil {
		source.Close()
		return nil, fmt.Errorf("acquire loop device: %w", err)
	}

	originSize, err := devicemapper.OriginSizeBytes(originLoop)
	if err != nil {
		source.Close()
		m.loops.Release(baseImagePath)
		return nil, fmt.Errorf("get origin size: %w", err)
	}

	// Move CoW file from snapshot dir to sandboxes dir for the running sandbox.
	savedCow := snapshot.CowPath(snapDir, sandboxID)
	cowPath := filepath.Join(m.cfg.SandboxesDir, fmt.Sprintf("%s.cow", sandboxID))
	if err := os.Rename(savedCow, cowPath); err != nil {
		source.Close()
		m.loops.Release(baseImagePath)
		return nil, fmt.Errorf("move cow file: %w", err)
	}

	// rollbackCow attempts to move the CoW file back to the snapshot dir.
	// Best-effort — logs a warning if it fails.
	rollbackCow := func() {
		if err := os.Rename(cowPath, savedCow); err != nil {
			slog.Warn("failed to rollback cow file", "src", cowPath, "dst", savedCow, "error", err)
		}
	}

	// Restore dm-snapshot from existing persistent CoW file.
	dmName := "wrenn-" + sandboxID
	dmDev, err := devicemapper.RestoreSnapshot(ctx, dmName, originLoop, cowPath, originSize)
	if err != nil {
		source.Close()
		m.loops.Release(baseImagePath)
		rollbackCow()
		return nil, fmt.Errorf("restore dm-snapshot: %w", err)
	}

	// Allocate network slot.
	slotIdx, err := m.slots.Allocate()
	if err != nil {
		source.Close()
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		rollbackCow()
		m.loops.Release(baseImagePath)
		return nil, fmt.Errorf("allocate network slot: %w", err)
	}
	slot := network.NewSlot(slotIdx)

	if err := network.CreateNetwork(slot); err != nil {
		source.Close()
		m.slots.Release(slotIdx)
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		rollbackCow()
		m.loops.Release(baseImagePath)
		return nil, fmt.Errorf("create network: %w", err)
	}

	// Start UFFD server.
	uffdSocketPath := filepath.Join(m.cfg.SandboxesDir, fmt.Sprintf("%s-uffd.sock", sandboxID))
	os.Remove(uffdSocketPath) // Clean stale socket.
	uffdServer := uffd.NewServer(uffdSocketPath, source)
	if err := uffdServer.Start(ctx); err != nil {
		source.Close()
		warnErr("network cleanup error", sandboxID, network.RemoveNetwork(slot))
		m.slots.Release(slotIdx)
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		rollbackCow()
		m.loops.Release(baseImagePath)
		return nil, fmt.Errorf("start uffd server: %w", err)
	}

	// Restore VM from snapshot.
	vmCfg := vm.VMConfig{
		SandboxID:        sandboxID,
		KernelPath:       m.cfg.KernelPath,
		RootfsPath:       dmDev.DevicePath,
		VCPUs:            1,                                         // Placeholder; overridden by snapshot.
		MemoryMB:         int(header.Metadata.Size / (1024 * 1024)), // Placeholder; overridden by snapshot.
		NetworkNamespace: slot.NamespaceID,
		TapDevice:        slot.TapName,
		TapMAC:           slot.TapMAC,
		GuestIP:          slot.GuestIP,
		GatewayIP:        slot.TapIP,
		NetMask:          slot.GuestNetMask,
	}

	snapPath := snapshot.SnapPath(snapDir, sandboxID)
	if _, err := m.vm.CreateFromSnapshot(ctx, vmCfg, snapPath, uffdSocketPath); err != nil {
		warnErr("uffd server stop error", sandboxID, uffdServer.Stop())
		source.Close()
		warnErr("network cleanup error", sandboxID, network.RemoveNetwork(slot))
		m.slots.Release(slotIdx)
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		rollbackCow()
		m.loops.Release(baseImagePath)
		return nil, fmt.Errorf("restore VM from snapshot: %w", err)
	}

	// Wait for envd to be ready.
	client := envdclient.New(slot.HostIP.String())
	waitCtx, waitCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
	defer waitCancel()

	if err := client.WaitUntilReady(waitCtx); err != nil {
		warnErr("uffd server stop error", sandboxID, uffdServer.Stop())
		source.Close()
		warnErr("vm destroy error", sandboxID, m.vm.Destroy(context.Background(), sandboxID))
		warnErr("network cleanup error", sandboxID, network.RemoveNetwork(slot))
		m.slots.Release(slotIdx)
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		rollbackCow()
		m.loops.Release(baseImagePath)
		return nil, fmt.Errorf("wait for envd: %w", err)
	}

	// Sync guest clock in background. Non-fatal — sandbox is usable before this completes.
	// Run in a goroutine so Init latency doesn't block the RPC response back to the control plane.
	go func() {
		initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer initCancel()
		if err := client.Init(initCtx); err != nil {
			slog.Warn("envd init (clock sync) failed", "sandbox", sandboxID, "error", err)
		}
	}()

	now := time.Now()
	sb := &sandboxState{
		Sandbox: models.Sandbox{
			ID:           sandboxID,
			Status:       models.StatusRunning,
			Template:     "",
			VCPUs:        vmCfg.VCPUs,
			MemoryMB:     vmCfg.MemoryMB,
			TimeoutSec:   timeoutSec,
			SlotIndex:    slotIdx,
			HostIP:       slot.HostIP,
			RootfsPath:   dmDev.DevicePath,
			CreatedAt:    now,
			LastActiveAt: now,
		},
		slot:           slot,
		client:         client,
		uffdSocketPath: uffdSocketPath,
		dmDevice:       dmDev,
		baseImagePath:  baseImagePath,
		// Preserve parent snapshot info so re-pause can use Diff snapshots.
		parent: &snapshotParent{
			header:    header,
			diffPaths: diffPaths,
		},
	}

	m.mu.Lock()
	m.boxes[sandboxID] = sb
	m.mu.Unlock()

	m.startSampler(sb)

	// Don't delete snapshot dir — diff files are needed for re-pause.
	// The CoW file was already moved out. The dir will be cleaned up
	// on destroy or overwritten on re-pause.

	slog.Info("sandbox resumed from snapshot",
		"id", sandboxID,
		"host_ip", slot.HostIP.String(),
		"dm_device", dmDev.DevicePath,
		"generation", header.Metadata.Generation,
	)

	return &sb.Sandbox, nil
}

// CreateSnapshot creates a reusable template from a sandbox. Works on both
// running and paused sandboxes. If the sandbox is running, it is paused first.
// The sandbox remains paused after this call (it can still be resumed).
//
// The rootfs is flattened (base + CoW merged) into a new standalone rootfs.ext4
// so the template has no dependency on the original base image. Memory state
// and VM snapshot files are copied as-is.
func (m *Manager) CreateSnapshot(ctx context.Context, sandboxID, name string) (int64, error) {
	if err := validate.SafeName(name); err != nil {
		return 0, fmt.Errorf("invalid snapshot name: %w", err)
	}

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

	// Create template directory.
	if err := snapshot.EnsureDir(m.cfg.ImagesDir, name); err != nil {
		return 0, fmt.Errorf("create template dir: %w", err)
	}

	// Copy VM snapshot file and memory header.
	srcDir := snapshot.DirPath(m.cfg.SnapshotsDir, sandboxID)
	dstDir := snapshot.DirPath(m.cfg.ImagesDir, name)

	for _, fname := range []string{snapshot.SnapFileName, snapshot.MemHeaderName} {
		src := filepath.Join(srcDir, fname)
		dst := filepath.Join(dstDir, fname)
		if err := copyFile(src, dst); err != nil {
			warnErr("template dir cleanup error", name, snapshot.Remove(m.cfg.ImagesDir, name))
			return 0, fmt.Errorf("copy %s: %w", fname, err)
		}
	}

	// Copy all memory diff files referenced by the header (supports multi-generation).
	headerData, err := os.ReadFile(filepath.Join(srcDir, snapshot.MemHeaderName))
	if err != nil {
		warnErr("template dir cleanup error", name, snapshot.Remove(m.cfg.ImagesDir, name))
		return 0, fmt.Errorf("read header for template: %w", err)
	}
	srcHeader, err := snapshot.Deserialize(headerData)
	if err != nil {
		warnErr("template dir cleanup error", name, snapshot.Remove(m.cfg.ImagesDir, name))
		return 0, fmt.Errorf("deserialize header for template: %w", err)
	}
	srcDiffPaths, err := snapshot.ListDiffFiles(m.cfg.SnapshotsDir, sandboxID, srcHeader)
	if err != nil {
		warnErr("template dir cleanup error", name, snapshot.Remove(m.cfg.ImagesDir, name))
		return 0, fmt.Errorf("list diff files for template: %w", err)
	}
	for _, srcPath := range srcDiffPaths {
		dstPath := filepath.Join(dstDir, filepath.Base(srcPath))
		if err := copyFile(srcPath, dstPath); err != nil {
			warnErr("template dir cleanup error", name, snapshot.Remove(m.cfg.ImagesDir, name))
			return 0, fmt.Errorf("copy diff file %s: %w", filepath.Base(srcPath), err)
		}
	}

	// Flatten rootfs: temporarily set up dm device from base + CoW, dd to new image.
	meta, err := snapshot.ReadMeta(m.cfg.SnapshotsDir, sandboxID)
	if err != nil {
		warnErr("template dir cleanup error", name, snapshot.Remove(m.cfg.ImagesDir, name))
		return 0, fmt.Errorf("read rootfs meta: %w", err)
	}

	originLoop, err := m.loops.Acquire(meta.BaseTemplate)
	if err != nil {
		warnErr("template dir cleanup error", name, snapshot.Remove(m.cfg.ImagesDir, name))
		return 0, fmt.Errorf("acquire loop device for flatten: %w", err)
	}

	originSize, err := devicemapper.OriginSizeBytes(originLoop)
	if err != nil {
		m.loops.Release(meta.BaseTemplate)
		warnErr("template dir cleanup error", name, snapshot.Remove(m.cfg.ImagesDir, name))
		return 0, fmt.Errorf("get origin size: %w", err)
	}

	// Temporarily restore the dm-snapshot to read the merged view.
	cowPath := snapshot.CowPath(m.cfg.SnapshotsDir, sandboxID)
	tmpDmName := "wrenn-flatten-" + sandboxID
	tmpDev, err := devicemapper.RestoreSnapshot(ctx, tmpDmName, originLoop, cowPath, originSize)
	if err != nil {
		m.loops.Release(meta.BaseTemplate)
		warnErr("template dir cleanup error", name, snapshot.Remove(m.cfg.ImagesDir, name))
		return 0, fmt.Errorf("restore dm-snapshot for flatten: %w", err)
	}

	// Flatten to new standalone rootfs.
	flattenedPath := snapshot.RootfsPath(m.cfg.ImagesDir, name)
	flattenErr := devicemapper.FlattenSnapshot(tmpDev.DevicePath, flattenedPath)

	// Always clean up the temporary dm device.
	warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), tmpDev))
	m.loops.Release(meta.BaseTemplate)

	if flattenErr != nil {
		warnErr("template dir cleanup error", name, snapshot.Remove(m.cfg.ImagesDir, name))
		return 0, fmt.Errorf("flatten rootfs: %w", flattenErr)
	}

	sizeBytes, err := snapshot.DirSize(m.cfg.ImagesDir, name)
	if err != nil {
		slog.Warn("failed to calculate snapshot size", "error", err)
	}

	slog.Info("template snapshot created (rootfs flattened)",
		"sandbox", sandboxID,
		"name", name,
		"size_bytes", sizeBytes,
	)
	return sizeBytes, nil
}

// DeleteSnapshot removes a snapshot template from disk.
func (m *Manager) DeleteSnapshot(name string) error {
	if err := validate.SafeName(name); err != nil {
		return fmt.Errorf("invalid snapshot name: %w", err)
	}
	return snapshot.Remove(m.cfg.ImagesDir, name)
}

// createFromSnapshot creates a new sandbox by restoring from a snapshot template
// in ImagesDir/{snapshotName}/. Uses UFFD for lazy memory loading.
// The template's rootfs.ext4 is a flattened standalone image — we create a
// dm-snapshot on top of it just like a normal Create.
func (m *Manager) createFromSnapshot(ctx context.Context, sandboxID, snapshotName string, vcpus, _, timeoutSec int) (*models.Sandbox, error) {
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

	// Snapshot determines memory size.
	memoryMB := int(header.Metadata.Size / (1024 * 1024))

	// Build diff file map — supports multi-generation templates.
	diffPaths, err := snapshot.ListDiffFiles(imagesDir, snapshotName, header)
	if err != nil {
		return nil, fmt.Errorf("list diff files: %w", err)
	}

	source, err := uffd.NewDiffFileSource(header, diffPaths)
	if err != nil {
		return nil, fmt.Errorf("create memory source: %w", err)
	}

	// Set up dm-snapshot on the template's flattened rootfs.
	baseRootfs := snapshot.RootfsPath(imagesDir, snapshotName)
	originLoop, err := m.loops.Acquire(baseRootfs)
	if err != nil {
		source.Close()
		return nil, fmt.Errorf("acquire loop device: %w", err)
	}

	originSize, err := devicemapper.OriginSizeBytes(originLoop)
	if err != nil {
		source.Close()
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("get origin size: %w", err)
	}

	dmName := "wrenn-" + sandboxID
	cowPath := filepath.Join(m.cfg.SandboxesDir, fmt.Sprintf("%s.cow", sandboxID))
	dmDev, err := devicemapper.CreateSnapshot(dmName, originLoop, cowPath, originSize)
	if err != nil {
		source.Close()
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("create dm-snapshot: %w", err)
	}

	// Allocate network.
	slotIdx, err := m.slots.Allocate()
	if err != nil {
		source.Close()
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		os.Remove(cowPath)
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("allocate network slot: %w", err)
	}
	slot := network.NewSlot(slotIdx)

	if err := network.CreateNetwork(slot); err != nil {
		source.Close()
		m.slots.Release(slotIdx)
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		os.Remove(cowPath)
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("create network: %w", err)
	}

	// Start UFFD server.
	uffdSocketPath := filepath.Join(m.cfg.SandboxesDir, fmt.Sprintf("%s-uffd.sock", sandboxID))
	os.Remove(uffdSocketPath)
	uffdServer := uffd.NewServer(uffdSocketPath, source)
	if err := uffdServer.Start(ctx); err != nil {
		source.Close()
		warnErr("network cleanup error", sandboxID, network.RemoveNetwork(slot))
		m.slots.Release(slotIdx)
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		os.Remove(cowPath)
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("start uffd server: %w", err)
	}

	// Restore VM.
	vmCfg := vm.VMConfig{
		SandboxID:        sandboxID,
		KernelPath:       m.cfg.KernelPath,
		RootfsPath:       dmDev.DevicePath,
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
		warnErr("uffd server stop error", sandboxID, uffdServer.Stop())
		source.Close()
		warnErr("network cleanup error", sandboxID, network.RemoveNetwork(slot))
		m.slots.Release(slotIdx)
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		os.Remove(cowPath)
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("restore VM from snapshot: %w", err)
	}

	// Wait for envd.
	client := envdclient.New(slot.HostIP.String())
	waitCtx, waitCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
	defer waitCancel()

	if err := client.WaitUntilReady(waitCtx); err != nil {
		warnErr("uffd server stop error", sandboxID, uffdServer.Stop())
		source.Close()
		warnErr("vm destroy error", sandboxID, m.vm.Destroy(context.Background(), sandboxID))
		warnErr("network cleanup error", sandboxID, network.RemoveNetwork(slot))
		m.slots.Release(slotIdx)
		warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		os.Remove(cowPath)
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("wait for envd: %w", err)
	}

	// Sync guest clock in background. Non-fatal — sandbox is usable before this completes.
	// Run in a goroutine so Init latency doesn't block the RPC response back to the control plane.
	go func() {
		initCtx, initCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer initCancel()
		if err := client.Init(initCtx); err != nil {
			slog.Warn("envd init (clock sync) failed", "sandbox", sandboxID, "error", err)
		}
	}()

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
			RootfsPath:   dmDev.DevicePath,
			CreatedAt:    now,
			LastActiveAt: now,
		},
		slot:           slot,
		client:         client,
		uffdSocketPath: uffdSocketPath,
		dmDevice:       dmDev,
		baseImagePath:  baseRootfs,
		// Template-spawned sandboxes also get diff re-pause support.
		parent: &snapshotParent{
			header:    header,
			diffPaths: diffPaths,
		},
	}

	m.mu.Lock()
	m.boxes[sandboxID] = sb
	m.mu.Unlock()

	m.startSampler(sb)

	slog.Info("sandbox created from snapshot",
		"id", sandboxID,
		"snapshot", snapshotName,
		"host_ip", slot.HostIP.String(),
		"dm_device", dmDev.DevicePath,
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

// Ping resets the inactivity timer for a running sandbox.
func (m *Manager) Ping(sandboxID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sb, ok := m.boxes[sandboxID]
	if !ok {
		return fmt.Errorf("sandbox not found: %s", sandboxID)
	}
	if sb.Status != models.StatusRunning {
		return fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}
	sb.LastActiveAt = time.Now()
	return nil
}

// DrainAutoPausedIDs returns and clears the list of sandbox IDs that were
// automatically paused by the TTL reaper since the last call.
func (m *Manager) DrainAutoPausedIDs() []string {
	m.autoPausedMu.Lock()
	defer m.autoPausedMu.Unlock()

	ids := m.autoPausedIDs
	m.autoPausedIDs = nil
	return ids
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
		ticker := time.NewTicker(2 * time.Second)
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

func (m *Manager) reapExpired(_ context.Context) {
	m.mu.RLock()
	var expired []string
	now := time.Now()
	for id, sb := range m.boxes {
		if sb.TimeoutSec <= 0 {
			continue
		}
		if sb.Status != models.StatusRunning {
			continue
		}
		if now.Sub(sb.LastActiveAt) > time.Duration(sb.TimeoutSec)*time.Second {
			expired = append(expired, id)
		}
	}
	m.mu.RUnlock()

	for _, id := range expired {
		slog.Info("TTL expired, auto-pausing sandbox", "id", id)
		// Use a detached context so that an app shutdown does not cancel
		// a pause mid-flight, which would leave the VM frozen without a
		// valid snapshot.
		pauseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err := m.Pause(pauseCtx, id)
		cancel()
		if err != nil {
			slog.Warn("TTL auto-pause failed, destroying sandbox", "id", id, "error", err)
			if destroyErr := m.Destroy(context.Background(), id); destroyErr != nil {
				slog.Warn("TTL destroy after failed pause also failed", "id", id, "error", destroyErr)
			}
			continue
		}
		m.autoPausedMu.Lock()
		m.autoPausedIDs = append(m.autoPausedIDs, id)
		m.autoPausedMu.Unlock()
	}
}

// Shutdown destroys all sandboxes, releases loop devices, and stops the TTL reaper.
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

	m.loops.ReleaseAll()
}

// PauseAll pauses every running sandbox managed by this host agent.
// Called when the host loses connectivity to the control plane to avoid
// leaving running VMs unmanaged. It is best-effort: failures for individual
// sandboxes are logged but do not stop the rest.
func (m *Manager) PauseAll(ctx context.Context) {
	m.mu.RLock()
	ids := make([]string, 0, len(m.boxes))
	for id, sb := range m.boxes {
		if sb.Status == models.StatusRunning {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()

	slog.Info("pausing all running sandboxes due to CP connection loss", "count", len(ids))
	for _, sbID := range ids {
		if err := m.Pause(ctx, sbID); err != nil {
			slog.Warn("PauseAll: failed to pause sandbox", "id", sbID, "error", err)
		}
	}
}

// warnErr logs a warning if err is non-nil. Used for best-effort cleanup
// in error paths where the primary error has already been captured.
func warnErr(msg string, id string, err error) {
	if err != nil {
		slog.Warn(msg, "id", id, "error", err)
	}
}

// startSampler resolves the Firecracker PID and starts a background goroutine
// that samples CPU/mem/disk at 500ms intervals into the ring buffer.
// Must be called after the sandbox is registered in m.boxes.
func (m *Manager) startSampler(sb *sandboxState) {
	v, ok := m.vm.Get(sb.ID)
	if !ok {
		slog.Warn("metrics: VM not found, skipping sampler", "id", sb.ID)
		return
	}

	// v.PID() is the cmd.Process.Pid of the "unshare -m -- bash -c script"
	// invocation. Because unshare(2) modifies the current process's namespace
	// before exec-replacing itself with bash, and bash exec-replaces itself
	// with ip-netns-exec, which exec-replaces itself with firecracker, the
	// entire exec chain occupies the same PID. v.PID() IS the Firecracker PID.
	fcPID := v.PID()

	sb.fcPID = fcPID
	sb.ring = newMetricsRing()

	ctx, cancel := context.WithCancel(context.Background())
	sb.samplerCancel = cancel
	sb.samplerDone = make(chan struct{})

	// Read initial CPU counters for delta calculation.
	// Passed to goroutine as local state — no shared mutation.
	initialCPU, err := readCPUStat(fcPID)
	if err != nil {
		slog.Warn("metrics: could not read initial CPU stat", "id", sb.ID, "error", err)
	}

	go m.samplerLoop(ctx, sb, fcPID, sb.VCPUs, initialCPU)
}

// samplerLoop samples /proc metrics at 500ms intervals.
// lastCPU is goroutine-local to avoid shared-state races.
func (m *Manager) samplerLoop(ctx context.Context, sb *sandboxState, fcPID, vcpus int, lastCPU cpuStat) {
	defer close(sb.samplerDone)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	clkTck := 100.0 // sysconf(_SC_CLK_TCK), almost always 100 on Linux
	lastTime := time.Now()
	cpuInitialized := lastCPU != (cpuStat{})

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			elapsed := now.Sub(lastTime).Seconds()
			lastTime = now

			// CPU: delta jiffies / (elapsed * CLK_TCK * vcpus) * 100
			var cpuPct float64
			cur, err := readCPUStat(fcPID)
			if err == nil {
				if cpuInitialized && elapsed > 0 && vcpus > 0 {
					deltaJiffies := float64((cur.utime + cur.stime) - (lastCPU.utime + lastCPU.stime))
					cpuPct = (deltaJiffies / (elapsed * clkTck * float64(vcpus))) * 100.0
					if cpuPct > 100.0 {
						cpuPct = 100.0
					}
					if cpuPct < 0 {
						cpuPct = 0
					}
				}
				lastCPU = cur
				cpuInitialized = true
			}

			// Memory: VmRSS of the Firecracker process.
			memBytes, _ := readMemRSS(fcPID)

			// Disk: allocated bytes of the CoW sparse file.
			var diskBytes int64
			if sb.dmDevice != nil {
				diskBytes, _ = readDiskAllocated(sb.dmDevice.CowPath)
			}

			sb.ring.Push(MetricPoint{
				Timestamp: now,
				CPUPct:    cpuPct,
				MemBytes:  memBytes,
				DiskBytes: diskBytes,
			})
		}
	}
}

// stopSampler stops the metrics sampling goroutine and waits for it to exit.
func (m *Manager) stopSampler(sb *sandboxState) {
	if sb.samplerCancel != nil {
		sb.samplerCancel()
		<-sb.samplerDone
		sb.samplerCancel = nil
	}
}

// GetMetrics returns the ring buffer data for the given range tier.
// Valid ranges: "10m", "2h", "24h".
func (m *Manager) GetMetrics(sandboxID, rangeTier string) ([]MetricPoint, error) {
	m.mu.RLock()
	sb, ok := m.boxes[sandboxID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("sandbox not found: %s", sandboxID)
	}
	if sb.ring == nil {
		return nil, nil
	}

	// Map the requested range to the appropriate ring tier and time cutoff.
	var points []MetricPoint
	var cutoff time.Duration
	switch rangeTier {
	case "5m":
		points = sb.ring.Get10m()
		cutoff = 5 * time.Minute
	case "10m":
		points = sb.ring.Get10m()
		cutoff = 10 * time.Minute
	case "1h":
		points = sb.ring.Get2h()
		cutoff = 1 * time.Hour
	case "2h":
		points = sb.ring.Get2h()
		cutoff = 2 * time.Hour
	case "6h":
		points = sb.ring.Get24h()
		cutoff = 6 * time.Hour
	case "12h":
		points = sb.ring.Get24h()
		cutoff = 12 * time.Hour
	case "24h":
		points = sb.ring.Get24h()
		cutoff = 24 * time.Hour
	default:
		return nil, fmt.Errorf("invalid range: %s (valid: 5m, 10m, 1h, 2h, 6h, 12h, 24h)", rangeTier)
	}

	// Filter points to the requested time window.
	threshold := time.Now().Add(-cutoff)
	filtered := points[:0:0]
	for _, p := range points {
		if !p.Timestamp.Before(threshold) {
			filtered = append(filtered, p)
		}
	}
	return filtered, nil
}

// FlushMetrics returns all three tier ring buffers, clears the ring, and
// stops the sampler goroutine. Called by the control plane before pause/destroy.
func (m *Manager) FlushMetrics(sandboxID string) (pts10m, pts2h, pts24h []MetricPoint, err error) {
	m.mu.RLock()
	sb, ok := m.boxes[sandboxID]
	m.mu.RUnlock()
	if !ok {
		return nil, nil, nil, fmt.Errorf("sandbox not found: %s", sandboxID)
	}

	m.stopSampler(sb)
	if sb.ring == nil {
		return nil, nil, nil, nil
	}
	pts10m, pts2h, pts24h = sb.ring.Flush()
	return pts10m, pts2h, pts24h, nil
}

// copyFile copies a regular file from src to dst using streaming I/O.
func copyFile(src, dst string) error {
	sf, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer sf.Close()

	df, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	defer df.Close()

	if _, err := df.ReadFrom(sf); err != nil {
		os.Remove(dst)
		return fmt.Errorf("copy %s → %s: %w", src, dst, err)
	}
	return nil
}
