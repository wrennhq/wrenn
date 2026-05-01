package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/devicemapper"
	"git.omukk.dev/wrenn/wrenn/internal/envdclient"
	"git.omukk.dev/wrenn/wrenn/internal/layout"
	"git.omukk.dev/wrenn/wrenn/internal/models"
	"git.omukk.dev/wrenn/wrenn/internal/network"
	"git.omukk.dev/wrenn/wrenn/internal/snapshot"
	"git.omukk.dev/wrenn/wrenn/internal/uffd"
	"git.omukk.dev/wrenn/wrenn/internal/vm"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	envdpb "git.omukk.dev/wrenn/wrenn/proto/envd/gen"
)

// Config holds the paths and defaults for the sandbox manager.
type Config struct {
	WrennDir            string // root directory (e.g. /var/lib/wrenn); all sub-paths derived via layout package
	EnvdTimeout         time.Duration
	DefaultRootfsSizeMB int // target size for template rootfs images; 0 → DefaultDiskSizeMB

	// Resolved at startup by the host agent.
	KernelPath         string // path to the latest vmlinux-x.y.z
	KernelVersion      string // semver extracted from filename
	FirecrackerBin     string // path to the firecracker binary
	FirecrackerVersion string // semver from firecracker --version
	AgentVersion       string // host agent version (injected via ldflags)
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

	// onDestroy is called with the sandbox ID after cleanup completes.
	// Used by ProxyHandler to evict cached reverse proxies.
	onDestroy func(sandboxID string)
}

// SetOnDestroy registers a callback invoked after each sandbox is cleaned up.
func (m *Manager) SetOnDestroy(fn func(sandboxID string)) {
	m.onDestroy = fn
}

// sandboxState holds the runtime state for a single sandbox.
type sandboxState struct {
	models.Sandbox
	lifecycleMu    sync.Mutex // serializes Pause/Destroy/Resume on this sandbox
	slot           *network.Slot
	client         *envdclient.Client
	connTracker    *ConnTracker // tracks in-flight proxy connections for pre-pause drain
	uffdSocketPath string       // non-empty for sandboxes restored from snapshot
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
// before falling back to a Full snapshot to collapse the chain. Firecracker
// snapshot/restore of a Go process (envd) accumulates runtime memory state
// drift; empirically, ~10 diff-based cycles corrupt the Go page allocator.
// A Full snapshot resets the generation counter and produces a clean base,
// preventing the crash.
const maxDiffGenerations = 8

// buildMetadata constructs the metadata map with version information.
func (m *Manager) buildMetadata(envdVersion string) map[string]string {
	meta := map[string]string{
		"kernel_version":      m.cfg.KernelVersion,
		"firecracker_version": m.cfg.FirecrackerVersion,
		"agent_version":       m.cfg.AgentVersion,
	}
	if envdVersion != "" {
		meta["envd_version"] = envdVersion
	}
	return meta
}

// resolveKernelPath returns the kernel path for the given version hint.
// If the exact version exists on disk, it is used. Otherwise, falls back to
// the latest kernel (m.cfg.KernelPath).
func (m *Manager) resolveKernelPath(versionHint string) string {
	if versionHint == "" {
		return m.cfg.KernelPath
	}
	exact := layout.KernelPathVersioned(m.cfg.WrennDir, versionHint)
	if _, err := os.Stat(exact); err == nil {
		return exact
	}
	slog.Warn("requested kernel version not found, using latest",
		"requested", versionHint, "latest", m.cfg.KernelVersion)
	return m.cfg.KernelPath
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
		loops:  devicemapper.NewLoopRegistry(),
		boxes:  make(map[string]*sandboxState),
		stopCh: make(chan struct{}),
	}
}

// Create boots a new sandbox: clone rootfs, set up network, start VM, wait for envd.
// If sandboxID is empty, a new ID is generated.
func (m *Manager) Create(ctx context.Context, sandboxID string, teamID, templateID pgtype.UUID, vcpus, memoryMB, timeoutSec, diskSizeMB int) (*models.Sandbox, error) {
	if sandboxID == "" {
		sandboxID = id.FormatSandboxID(id.NewSandboxID())
	}

	if vcpus <= 0 {
		vcpus = 1
	}
	if memoryMB <= 0 {
		memoryMB = 512
	}
	if diskSizeMB <= 0 {
		diskSizeMB = 5120 // 5 GB default
	}

	// Check if template refers to a snapshot (has snapfile + memfile + header + rootfs).
	tmplDir := layout.TemplateDir(m.cfg.WrennDir, teamID, templateID)
	if _, err := os.Stat(filepath.Join(tmplDir, snapshot.SnapFileName)); err == nil {
		return m.createFromSnapshot(ctx, sandboxID, teamID, templateID, vcpus, memoryMB, timeoutSec, diskSizeMB)
	}

	// Resolve base rootfs image.
	baseRootfs := layout.TemplateRootfs(m.cfg.WrennDir, teamID, templateID)
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
	cowPath := filepath.Join(layout.SandboxesDir(m.cfg.WrennDir), fmt.Sprintf("%s.cow", sandboxID))
	cowSize := int64(diskSizeMB) * 1024 * 1024
	dmDev, err := devicemapper.CreateSnapshot(dmName, originLoop, cowPath, originSize, cowSize)
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
		TemplateID:       id.UUIDString(templateID),
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
		FirecrackerBin:   m.cfg.FirecrackerBin,
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

	// Fetch envd version (best-effort).
	envdVersion, _ := client.FetchVersion(ctx)

	now := time.Now()
	sb := &sandboxState{
		Sandbox: models.Sandbox{
			ID:             sandboxID,
			Status:         models.StatusRunning,
			TemplateTeamID: teamID.Bytes,
			TemplateID:     templateID.Bytes,
			VCPUs:          vcpus,
			MemoryMB:       memoryMB,
			TimeoutSec:     timeoutSec,
			SlotIndex:      slotIdx,
			HostIP:         slot.HostIP,
			RootfsPath:     dmDev.DevicePath,
			CreatedAt:      now,
			LastActiveAt:   now,
			Metadata:       m.buildMetadata(envdVersion),
		},
		slot:          slot,
		client:        client,
		connTracker:   &ConnTracker{},
		dmDevice:      dmDev,
		baseImagePath: baseRootfs,
	}

	m.mu.Lock()
	m.boxes[sandboxID] = sb
	m.mu.Unlock()

	m.startSampler(sb)

	slog.Info("sandbox created",
		"id", sandboxID,
		"team_id", teamID,
		"template_id", templateID,
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
		// Wait for any in-progress Pause to finish before tearing down resources.
		sb.lifecycleMu.Lock()
		defer sb.lifecycleMu.Unlock()
		m.cleanup(ctx, sb)
	}

	// Always clean up pause snapshot files (may exist if sandbox was paused).
	if err := os.RemoveAll(layout.PauseSnapshotDir(m.cfg.WrennDir, sandboxID)); err != nil {
		slog.Warn("snapshot cleanup error", "id", sandboxID, "error", err)
	}

	if m.onDestroy != nil {
		m.onDestroy(sandboxID)
	}

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

	// Serialize lifecycle operations on this sandbox to prevent concurrent
	// Pause/Destroy calls from corrupting Firecracker state.
	sb.lifecycleMu.Lock()
	defer sb.lifecycleMu.Unlock()

	if sb.Status != models.StatusRunning {
		return fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	// Stop the metrics sampler goroutine before tearing down any resources
	// it reads (dm device, Firecracker PID). Without this, the sampler
	// leaks on every successful pause.
	m.stopSampler(sb)

	// Step 0: Drain in-flight proxy connections before freezing vCPUs.
	// This prevents Go runtime corruption inside the guest caused by stale
	// TCP state from connections that were alive when the VM was snapshotted.
	sb.connTracker.Drain(2 * time.Second)
	slog.Debug("pause: proxy connections drained", "id", sandboxID)

	// Step 0b: Close host-side idle connections to envd. Done before
	// PrepareSnapshot so FIN packets propagate to the guest during the
	// PrepareSnapshot window (no extra sleep needed).
	sb.client.CloseIdleConnections()
	slog.Debug("pause: envd client idle connections closed", "id", sandboxID)

	// Step 0c: Signal envd to quiesce continuous goroutines (port scanner,
	// forwarder), close idle HTTP connections, and run GC before freezing
	// vCPUs. This prevents Go runtime page allocator corruption ("bad
	// summary data") on snapshot restore. The 3s timeout also gives time
	// for the FINs from Step 0b to be processed by the guest kernel.
	// Best-effort: a failure is logged but does not abort the pause.
	func() {
		prepCtx, prepCancel := context.WithTimeout(ctx, 3*time.Second)
		defer prepCancel()
		if err := sb.client.PrepareSnapshot(prepCtx); err != nil {
			slog.Warn("pause: pre-snapshot quiesce failed (best-effort)", "id", sandboxID, "error", err)
		} else {
			slog.Debug("pause: envd goroutines quiesced", "id", sandboxID)
		}
	}()

	pauseStart := time.Now()

	// Step 1: Pause the VM (freeze vCPUs).
	if err := m.vm.Pause(ctx, sandboxID); err != nil {
		sb.connTracker.Reset()
		return fmt.Errorf("pause VM: %w", err)
	}
	slog.Debug("pause: VM paused", "id", sandboxID, "elapsed", time.Since(pauseStart))

	// Always use Diff when we have a parent snapshot — Diff only captures
	// changed pages and is much faster than Full (which dumps all memory).
	// For first-time pauses (no parent) we must use Full.
	snapshotType := "Full"
	if sb.parent != nil {
		snapshotType = "Diff"
	}

	// resumeOnError unpauses the VM so the sandbox stays usable when a
	// post-freeze step fails. If the resume itself fails, the sandbox is
	// left frozen — the caller should destroy it. It also resets the
	// connection tracker so the sandbox can accept proxy connections again.
	resumeOnError := func() {
		sb.connTracker.Reset()
		if err := m.vm.Resume(ctx, sandboxID); err != nil {
			slog.Error("failed to resume VM after pause error — sandbox is frozen", "id", sandboxID, "error", err)
		}
	}

	// Step 2: Take VM state snapshot (snapfile + memfile).
	pauseDir := layout.PauseSnapshotDir(m.cfg.WrennDir, sandboxID)
	if err := os.MkdirAll(pauseDir, 0755); err != nil {
		resumeOnError()
		return fmt.Errorf("create snapshot dir: %w", err)
	}

	rawMemPath := filepath.Join(pauseDir, "memfile.raw")
	snapPath := filepath.Join(pauseDir, snapshot.SnapFileName)

	snapshotStart := time.Now()
	if err := m.vm.Snapshot(ctx, sandboxID, snapPath, rawMemPath, snapshotType); err != nil {
		warnErr("snapshot dir cleanup error", sandboxID, os.RemoveAll(pauseDir))
		resumeOnError()
		return fmt.Errorf("create VM snapshot: %w", err)
	}
	slog.Debug("pause: FC snapshot created", "id", sandboxID, "type", snapshotType, "elapsed", time.Since(snapshotStart))

	// Step 3: Process the raw memfile into a compact diff + header.
	buildID := uuid.New()
	headerPath := filepath.Join(pauseDir, snapshot.MemHeaderName)

	processStart := time.Now()
	if sb.parent != nil {
		// Diff: process against parent header, producing only changed blocks.
		diffPath := snapshot.MemDiffPathForBuild(pauseDir, "", buildID)
		if _, err := snapshot.ProcessMemfileWithParent(rawMemPath, diffPath, headerPath, sb.parent.header, buildID); err != nil {
			warnErr("snapshot dir cleanup error", sandboxID, os.RemoveAll(pauseDir))
			resumeOnError()
			return fmt.Errorf("process memfile with parent: %w", err)
		}

		// Copy previous generation diff files into the snapshot directory.
		for prevBuildID, prevPath := range sb.parent.diffPaths {
			dstPath := snapshot.MemDiffPathForBuild(pauseDir, "", uuid.MustParse(prevBuildID))
			if prevPath != dstPath {
				if err := copyFile(prevPath, dstPath); err != nil {
					warnErr("snapshot dir cleanup error", sandboxID, os.RemoveAll(pauseDir))
					resumeOnError()
					return fmt.Errorf("copy parent diff file: %w", err)
				}
			}
		}

		// If the generation cap is reached, merge all diff files into a
		// single file to collapse the chain. This is a file-level operation
		// (no Firecracker involvement) so it's fast and reliable.
		generation := sb.parent.header.Metadata.Generation + 1
		if generation >= maxDiffGenerations {
			slog.Debug("pause: merging diff generations", "id", sandboxID, "generation", generation)

			// Load the header we just wrote (it references all generations).
			headerData, err := os.ReadFile(headerPath)
			if err != nil {
				warnErr("snapshot dir cleanup error", sandboxID, os.RemoveAll(pauseDir))
				resumeOnError()
				return fmt.Errorf("read header for merge: %w", err)
			}
			currentHeader, err := snapshot.Deserialize(headerData)
			if err != nil {
				warnErr("snapshot dir cleanup error", sandboxID, os.RemoveAll(pauseDir))
				resumeOnError()
				return fmt.Errorf("deserialize header for merge: %w", err)
			}

			// Locate all diff files referenced by the header.
			diffFiles, err := snapshot.ListDiffFiles(pauseDir, "", currentHeader)
			if err != nil {
				warnErr("snapshot dir cleanup error", sandboxID, os.RemoveAll(pauseDir))
				resumeOnError()
				return fmt.Errorf("list diff files for merge: %w", err)
			}

			// Merge into a single new diff file.
			mergedPath := snapshot.MemDiffPath(pauseDir, "")
			if _, err := snapshot.MergeDiffs(currentHeader, diffFiles, mergedPath, headerPath); err != nil {
				warnErr("snapshot dir cleanup error", sandboxID, os.RemoveAll(pauseDir))
				resumeOnError()
				return fmt.Errorf("merge diff files: %w", err)
			}

			// Remove the old per-generation diff files.
			removeStaleMemDiffs(pauseDir)
			slog.Debug("pause: diff merge complete", "id", sandboxID)
		}
	} else {
		// Full: first pause — no parent to diff against.
		diffPath := snapshot.MemDiffPath(pauseDir, "")
		if _, err := snapshot.ProcessMemfile(rawMemPath, diffPath, headerPath, buildID); err != nil {
			warnErr("snapshot dir cleanup error", sandboxID, os.RemoveAll(pauseDir))
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
			warnErr("snapshot dir cleanup error", sandboxID, os.RemoveAll(pauseDir))
			m.mu.Lock()
			delete(m.boxes, sandboxID)
			m.mu.Unlock()
			return fmt.Errorf("remove dm-snapshot: %w", err)
		}

		// Move (not copy) the CoW file into the snapshot directory.
		snapshotCow := snapshot.CowPath(pauseDir, "")
		if err := os.Rename(sb.dmDevice.CowPath, snapshotCow); err != nil {
			warnErr("snapshot dir cleanup error", sandboxID, os.RemoveAll(pauseDir))
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
		if err := snapshot.WriteMeta(pauseDir, "", &snapshot.RootfsMeta{
			BaseTemplate: sb.baseImagePath,
			TemplateID:   uuid.UUID(sb.TemplateID).String(),
		}); err != nil {
			warnErr("snapshot dir cleanup error", sandboxID, os.RemoveAll(pauseDir))
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
func (m *Manager) Resume(ctx context.Context, sandboxID string, timeoutSec int, kernelVersion string) (*models.Sandbox, error) {
	pauseDir := layout.PauseSnapshotDir(m.cfg.WrennDir, sandboxID)
	if _, err := os.Stat(pauseDir); err != nil {
		return nil, fmt.Errorf("no snapshot found for sandbox %s", sandboxID)
	}

	// Read the header to set up the UFFD memory source.
	headerData, err := os.ReadFile(filepath.Join(pauseDir, snapshot.MemHeaderName))
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	header, err := snapshot.Deserialize(headerData)
	if err != nil {
		return nil, fmt.Errorf("deserialize header: %w", err)
	}

	// Build diff file map — supports both single-generation and multi-generation.
	diffPaths, err := snapshot.ListDiffFiles(pauseDir, "", header)
	if err != nil {
		return nil, fmt.Errorf("list diff files: %w", err)
	}

	source, err := uffd.NewDiffFileSource(header, diffPaths)
	if err != nil {
		return nil, fmt.Errorf("create memory source: %w", err)
	}

	// Read rootfs metadata to find the base template image.
	meta, err := snapshot.ReadMeta(pauseDir, "")
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
	savedCow := snapshot.CowPath(pauseDir, "")
	cowPath := filepath.Join(layout.SandboxesDir(m.cfg.WrennDir), fmt.Sprintf("%s.cow", sandboxID))
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
	uffdSocketPath := filepath.Join(layout.SandboxesDir(m.cfg.WrennDir), fmt.Sprintf("%s-uffd.sock", sandboxID))
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
		TemplateID:       meta.TemplateID,
		KernelPath:       m.resolveKernelPath(kernelVersion),
		RootfsPath:       dmDev.DevicePath,
		VCPUs:            1,                                         // Placeholder; overridden by snapshot.
		MemoryMB:         int(header.Metadata.Size / (1024 * 1024)), // Placeholder; overridden by snapshot.
		NetworkNamespace: slot.NamespaceID,
		TapDevice:        slot.TapName,
		TapMAC:           slot.TapMAC,
		GuestIP:          slot.GuestIP,
		GatewayIP:        slot.TapIP,
		NetMask:          slot.GuestNetMask,
		FirecrackerBin:   m.cfg.FirecrackerBin,
	}

	resumeSnapPath := filepath.Join(pauseDir, snapshot.SnapFileName)
	if _, err := m.vm.CreateFromSnapshot(ctx, vmCfg, resumeSnapPath, uffdSocketPath); err != nil {
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

	// Trigger envd to re-read MMDS so it picks up the new sandbox/template IDs.
	if err := client.PostInit(waitCtx); err != nil {
		slog.Warn("post-init failed after resume, metadata files may be stale", "sandbox", sandboxID, "error", err)
	}

	// Fetch envd version (best-effort).
	envdVersion, _ := client.FetchVersion(ctx)

	now := time.Now()
	sb := &sandboxState{
		Sandbox: models.Sandbox{
			ID:           sandboxID,
			Status:       models.StatusRunning,
			VCPUs:        vmCfg.VCPUs,
			MemoryMB:     vmCfg.MemoryMB,
			TimeoutSec:   timeoutSec,
			SlotIndex:    slotIdx,
			HostIP:       slot.HostIP,
			RootfsPath:   dmDev.DevicePath,
			CreatedAt:    now,
			LastActiveAt: now,
			Metadata:     m.buildMetadata(envdVersion),
		},
		slot:           slot,
		client:         client,
		connTracker:    &ConnTracker{},
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
func (m *Manager) CreateSnapshot(ctx context.Context, sandboxID string, teamID, templateID pgtype.UUID) (int64, error) {
	// If the sandbox is running, pause it first.
	if _, err := m.get(sandboxID); err == nil {
		if err := m.Pause(ctx, sandboxID); err != nil {
			return 0, fmt.Errorf("pause sandbox: %w", err)
		}
	}

	// At this point, pause snapshot files must exist.
	pauseDir := layout.PauseSnapshotDir(m.cfg.WrennDir, sandboxID)
	if _, err := os.Stat(pauseDir); err != nil {
		return 0, fmt.Errorf("no snapshot found for sandbox %s", sandboxID)
	}

	// Create template directory.
	dstDir := layout.TemplateDir(m.cfg.WrennDir, teamID, templateID)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return 0, fmt.Errorf("create template dir: %w", err)
	}

	// Copy VM snapshot file and memory header.
	srcDir := pauseDir

	for _, fname := range []string{snapshot.SnapFileName, snapshot.MemHeaderName} {
		src := filepath.Join(srcDir, fname)
		dst := filepath.Join(dstDir, fname)
		if err := copyFile(src, dst); err != nil {
			warnErr("template dir cleanup error", dstDir, os.RemoveAll(dstDir))
			return 0, fmt.Errorf("copy %s: %w", fname, err)
		}
	}

	// Copy all memory diff files referenced by the header (supports multi-generation).
	headerData, err := os.ReadFile(filepath.Join(srcDir, snapshot.MemHeaderName))
	if err != nil {
		warnErr("template dir cleanup error", dstDir, os.RemoveAll(dstDir))
		return 0, fmt.Errorf("read header for template: %w", err)
	}
	srcHeader, err := snapshot.Deserialize(headerData)
	if err != nil {
		warnErr("template dir cleanup error", dstDir, os.RemoveAll(dstDir))
		return 0, fmt.Errorf("deserialize header for template: %w", err)
	}
	srcDiffPaths, err := snapshot.ListDiffFiles(pauseDir, "", srcHeader)
	if err != nil {
		warnErr("template dir cleanup error", dstDir, os.RemoveAll(dstDir))
		return 0, fmt.Errorf("list diff files for template: %w", err)
	}
	for _, srcPath := range srcDiffPaths {
		dstPath := filepath.Join(dstDir, filepath.Base(srcPath))
		if err := copyFile(srcPath, dstPath); err != nil {
			warnErr("template dir cleanup error", dstDir, os.RemoveAll(dstDir))
			return 0, fmt.Errorf("copy diff file %s: %w", filepath.Base(srcPath), err)
		}
	}

	// Flatten rootfs: temporarily set up dm device from base + CoW, dd to new image.
	meta, err := snapshot.ReadMeta(pauseDir, "")
	if err != nil {
		warnErr("template dir cleanup error", dstDir, os.RemoveAll(dstDir))
		return 0, fmt.Errorf("read rootfs meta: %w", err)
	}

	originLoop, err := m.loops.Acquire(meta.BaseTemplate)
	if err != nil {
		warnErr("template dir cleanup error", dstDir, os.RemoveAll(dstDir))
		return 0, fmt.Errorf("acquire loop device for flatten: %w", err)
	}

	originSize, err := devicemapper.OriginSizeBytes(originLoop)
	if err != nil {
		m.loops.Release(meta.BaseTemplate)
		warnErr("template dir cleanup error", dstDir, os.RemoveAll(dstDir))
		return 0, fmt.Errorf("get origin size: %w", err)
	}

	// Temporarily restore the dm-snapshot to read the merged view.
	cowPath := snapshot.CowPath(pauseDir, "")
	tmpDmName := "wrenn-flatten-" + sandboxID
	tmpDev, err := devicemapper.RestoreSnapshot(ctx, tmpDmName, originLoop, cowPath, originSize)
	if err != nil {
		m.loops.Release(meta.BaseTemplate)
		warnErr("template dir cleanup error", dstDir, os.RemoveAll(dstDir))
		return 0, fmt.Errorf("restore dm-snapshot for flatten: %w", err)
	}

	// Flatten to new standalone rootfs.
	flattenedPath := filepath.Join(dstDir, snapshot.RootfsFileName)
	flattenErr := devicemapper.FlattenSnapshot(tmpDev.DevicePath, flattenedPath)

	// Always clean up the temporary dm device.
	warnErr("dm-snapshot remove error", sandboxID, devicemapper.RemoveSnapshot(context.Background(), tmpDev))
	m.loops.Release(meta.BaseTemplate)

	if flattenErr != nil {
		warnErr("template dir cleanup error", dstDir, os.RemoveAll(dstDir))
		return 0, fmt.Errorf("flatten rootfs: %w", flattenErr)
	}

	sizeBytes, err := snapshot.DirSize(dstDir, "")
	if err != nil {
		slog.Warn("failed to calculate snapshot size", "error", err)
	}

	slog.Info("template snapshot created (rootfs flattened)",
		"sandbox", sandboxID,
		"team_id", teamID,
		"template_id", templateID,
		"size_bytes", sizeBytes,
	)
	return sizeBytes, nil
}

// FlattenRootfs stops a running sandbox, flattens its device-mapper CoW
// rootfs into a standalone rootfs.ext4, and cleans up all resources.
// The result is an image-only template (no VM memory/CPU state) stored in
// ImagesDir/{name}/rootfs.ext4.
func (m *Manager) FlattenRootfs(ctx context.Context, sandboxID string, teamID, templateID pgtype.UUID) (int64, error) {
	m.mu.Lock()
	sb, ok := m.boxes[sandboxID]
	if ok {
		delete(m.boxes, sandboxID)
	}
	m.mu.Unlock()

	if !ok {
		return 0, fmt.Errorf("sandbox %s not found", sandboxID)
	}

	// Flush guest page cache to disk before stopping the VM. Without this,
	// files written by the build (e.g. pip-installed packages) may exist in the
	// guest's page cache but not yet on the dm block device — flatten would then
	// capture 0-byte files.
	func() {
		syncCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if _, err := sb.client.Exec(syncCtx, "/bin/sync"); err != nil {
			slog.Warn("flatten: guest sync failed (non-fatal)", "id", sb.ID, "error", err)
		}
	}()

	// Stop the VM but keep the dm device alive for flattening.
	m.stopSampler(sb)
	if err := m.vm.Destroy(ctx, sb.ID); err != nil {
		slog.Warn("vm destroy error during flatten", "id", sb.ID, "error", err)
	}

	// Release network resources — not needed after VM is stopped.
	if err := network.RemoveNetwork(sb.slot); err != nil {
		slog.Warn("network cleanup error during flatten", "id", sb.ID, "error", err)
	}
	m.slots.Release(sb.SlotIndex)

	if sb.uffdSocketPath != "" {
		os.Remove(sb.uffdSocketPath)
	}

	// Create template directory and flatten the dm-snapshot.
	flattenDstDir := layout.TemplateDir(m.cfg.WrennDir, teamID, templateID)
	if err := os.MkdirAll(flattenDstDir, 0755); err != nil {
		m.cleanupDM(sb)
		return 0, fmt.Errorf("create template dir: %w", err)
	}

	outputPath := filepath.Join(flattenDstDir, snapshot.RootfsFileName)
	if sb.dmDevice == nil {
		m.cleanupDM(sb)
		warnErr("template dir cleanup error", flattenDstDir, os.RemoveAll(flattenDstDir))
		return 0, fmt.Errorf("sandbox %s has no dm device", sandboxID)
	}

	if err := devicemapper.FlattenSnapshot(sb.dmDevice.DevicePath, outputPath); err != nil {
		m.cleanupDM(sb)
		warnErr("template dir cleanup error", flattenDstDir, os.RemoveAll(flattenDstDir))
		return 0, fmt.Errorf("flatten rootfs: %w", err)
	}

	// Clean up dm device and loop device now that flatten is complete.
	m.cleanupDM(sb)

	// Shrink the flattened image to its minimum size, then re-expand to the
	// configured default rootfs size so sandboxes see the full disk from boot.
	if out, err := exec.Command("e2fsck", "-fy", outputPath).CombinedOutput(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() > 1 {
			slog.Warn("e2fsck before shrink failed (non-fatal)", "output", string(out), "error", err)
		}
	}
	if out, err := exec.Command("resize2fs", "-M", outputPath).CombinedOutput(); err != nil {
		slog.Warn("resize2fs -M failed (non-fatal)", "output", string(out), "error", err)
	}

	// Re-expand to default rootfs size.
	targetMB := m.cfg.DefaultRootfsSizeMB
	if targetMB <= 0 {
		targetMB = DefaultDiskSizeMB
	}
	if err := expandImage(outputPath, int64(targetMB)*1024*1024, targetMB); err != nil {
		slog.Warn("failed to expand template to default size (non-fatal)", "error", err)
	}

	sizeBytes, err := snapshot.DirSize(flattenDstDir, "")
	if err != nil {
		slog.Warn("failed to calculate template size", "error", err)
	}

	slog.Info("rootfs flattened to image-only template",
		"sandbox", sandboxID,
		"team_id", teamID,
		"template_id", templateID,
		"size_bytes", sizeBytes,
	)
	return sizeBytes, nil
}

// cleanupDM tears down the dm-snapshot device and releases the base image loop device.
func (m *Manager) cleanupDM(sb *sandboxState) {
	if sb.dmDevice != nil {
		if err := devicemapper.RemoveSnapshot(context.Background(), sb.dmDevice); err != nil {
			slog.Warn("dm-snapshot remove error", "id", sb.ID, "error", err)
		}
		os.Remove(sb.dmDevice.CowPath)
	}
	if sb.baseImagePath != "" {
		m.loops.Release(sb.baseImagePath)
	}
}

// DeleteSnapshot removes a snapshot template from disk.
func (m *Manager) DeleteSnapshot(teamID, templateID pgtype.UUID) error {
	return os.RemoveAll(layout.TemplateDir(m.cfg.WrennDir, teamID, templateID))
}

// createFromSnapshot creates a new sandbox by restoring from a snapshot template
// in ImagesDir/{snapshotName}/. Uses UFFD for lazy memory loading.
// The template's rootfs.ext4 is a flattened standalone image — we create a
// dm-snapshot on top of it just like a normal Create.
func (m *Manager) createFromSnapshot(ctx context.Context, sandboxID string, teamID, templateID pgtype.UUID, vcpus, _, timeoutSec, diskSizeMB int) (*models.Sandbox, error) {
	tmplDir := layout.TemplateDir(m.cfg.WrennDir, teamID, templateID)

	// Read the header.
	headerData, err := os.ReadFile(filepath.Join(tmplDir, snapshot.MemHeaderName))
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
	diffPaths, err := snapshot.ListDiffFiles(tmplDir, "", header)
	if err != nil {
		return nil, fmt.Errorf("list diff files: %w", err)
	}

	source, err := uffd.NewDiffFileSource(header, diffPaths)
	if err != nil {
		return nil, fmt.Errorf("create memory source: %w", err)
	}

	// Set up dm-snapshot on the template's flattened rootfs.
	baseRootfs := filepath.Join(tmplDir, snapshot.RootfsFileName)
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
	cowPath := filepath.Join(layout.SandboxesDir(m.cfg.WrennDir), fmt.Sprintf("%s.cow", sandboxID))
	cowSize := int64(diskSizeMB) * 1024 * 1024
	dmDev, err := devicemapper.CreateSnapshot(dmName, originLoop, cowPath, originSize, cowSize)
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
	uffdSocketPath := filepath.Join(layout.SandboxesDir(m.cfg.WrennDir), fmt.Sprintf("%s-uffd.sock", sandboxID))
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
		TemplateID:       id.UUIDString(templateID),
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
		FirecrackerBin:   m.cfg.FirecrackerBin,
	}

	snapPath := filepath.Join(tmplDir, snapshot.SnapFileName)
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

	// Trigger envd to re-read MMDS so it picks up the new sandbox/template IDs.
	if err := client.PostInit(waitCtx); err != nil {
		slog.Warn("post-init failed after template restore, metadata files may be stale", "sandbox", sandboxID, "error", err)
	}

	// Fetch envd version (best-effort).
	envdVersion, _ := client.FetchVersion(ctx)

	now := time.Now()
	sb := &sandboxState{
		Sandbox: models.Sandbox{
			ID:             sandboxID,
			Status:         models.StatusRunning,
			TemplateTeamID: teamID.Bytes,
			TemplateID:     templateID.Bytes,
			VCPUs:          vcpus,
			MemoryMB:       memoryMB,
			TimeoutSec:     timeoutSec,
			SlotIndex:      slotIdx,
			HostIP:         slot.HostIP,
			RootfsPath:     dmDev.DevicePath,
			CreatedAt:      now,
			LastActiveAt:   now,
			Metadata:       m.buildMetadata(envdVersion),
		},
		slot:           slot,
		client:         client,
		connTracker:    &ConnTracker{},
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
		"team_id", teamID,
		"template_id", templateID,
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

// SetDefaults calls envd's PostInit to configure the default user and
// environment variables for a running sandbox. This is called by the host
// agent after sandbox creation or resume when the template specifies defaults.
func (m *Manager) SetDefaults(ctx context.Context, sandboxID, defaultUser string, defaultEnv map[string]string) error {
	if defaultUser == "" && len(defaultEnv) == 0 {
		return nil
	}
	sb, err := m.get(sandboxID)
	if err != nil {
		return err
	}
	if sb.Status != models.StatusRunning {
		return fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}
	return sb.client.PostInitWithDefaults(ctx, defaultUser, defaultEnv)
}

// PtyAttach starts a new PTY process or reconnects to an existing one.
// If cmd is non-empty, starts a new process. If empty, reconnects using tag.
func (m *Manager) PtyAttach(ctx context.Context, sandboxID, tag, cmd string, args []string, cols, rows uint32, envs map[string]string, cwd string) (<-chan envdclient.PtyEvent, error) {
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

	if cmd != "" {
		return sb.client.PtyStart(ctx, tag, cmd, args, cols, rows, envs, cwd)
	}
	return sb.client.PtyConnect(ctx, tag)
}

// PtySendInput sends raw bytes to a PTY process in a sandbox.
func (m *Manager) PtySendInput(ctx context.Context, sandboxID, tag string, data []byte) error {
	sb, err := m.get(sandboxID)
	if err != nil {
		return err
	}
	if sb.Status != models.StatusRunning {
		return fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	m.mu.Lock()
	sb.LastActiveAt = time.Now()
	m.mu.Unlock()

	return sb.client.PtySendInput(ctx, tag, data)
}

// PtyResize updates the terminal dimensions for a PTY process in a sandbox.
func (m *Manager) PtyResize(ctx context.Context, sandboxID, tag string, cols, rows uint32) error {
	sb, err := m.get(sandboxID)
	if err != nil {
		return err
	}
	if sb.Status != models.StatusRunning {
		return fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	return sb.client.PtyResize(ctx, tag, cols, rows)
}

// PtyKill sends SIGKILL to a PTY process in a sandbox.
func (m *Manager) PtyKill(ctx context.Context, sandboxID, tag string) error {
	sb, err := m.get(sandboxID)
	if err != nil {
		return err
	}
	if sb.Status != models.StatusRunning {
		return fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	return sb.client.PtyKill(ctx, tag)
}

// StartBackground starts a background process inside a sandbox.
func (m *Manager) StartBackground(ctx context.Context, sandboxID, tag, cmd string, args []string, envs map[string]string, cwd string) (uint32, error) {
	sb, err := m.get(sandboxID)
	if err != nil {
		return 0, err
	}
	if sb.Status != models.StatusRunning {
		return 0, fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	m.mu.Lock()
	sb.LastActiveAt = time.Now()
	m.mu.Unlock()

	return sb.client.StartBackground(ctx, tag, cmd, args, envs, cwd)
}

// ConnectProcess re-attaches to a running process inside a sandbox.
func (m *Manager) ConnectProcess(ctx context.Context, sandboxID string, pid uint32, tag string) (<-chan envdclient.ExecStreamEvent, error) {
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

	return sb.client.ConnectProcess(ctx, pid, tag)
}

// ListProcesses returns all running processes inside a sandbox.
func (m *Manager) ListProcesses(ctx context.Context, sandboxID string) ([]envdclient.ProcessInfo, error) {
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

	return sb.client.ListProcesses(ctx)
}

// KillProcess sends a signal to a process inside a sandbox.
func (m *Manager) KillProcess(ctx context.Context, sandboxID string, pid uint32, tag string, signal envdpb.Signal) error {
	sb, err := m.get(sandboxID)
	if err != nil {
		return err
	}
	if sb.Status != models.StatusRunning {
		return fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	m.mu.Lock()
	sb.LastActiveAt = time.Now()
	m.mu.Unlock()

	return sb.client.KillProcess(ctx, pid, tag, signal)
}

// AcquireProxyConn atomically looks up a sandbox by ID and registers an
// in-flight proxy connection. Returns the sandbox's host-reachable IP, the
// connection tracker, and true on success. The caller must call
// tracker.Release() when the request completes. Returns zero values and
// false if the sandbox is not found, not running, or is draining for a pause.
func (m *Manager) AcquireProxyConn(sandboxID string) (net.IP, *ConnTracker, bool) {
	m.mu.RLock()
	sb, ok := m.boxes[sandboxID]
	m.mu.RUnlock()

	if !ok || sb.Status != models.StatusRunning {
		return nil, nil, false
	}
	if !sb.connTracker.Acquire() {
		return nil, nil, false
	}
	return sb.HostIP, sb.connTracker, true
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

// removeStaleMemDiffs removes memfile.{uuid} diff files from a snapshot
// directory. Called before writing a Full snapshot to prevent orphaned diffs
// from accumulating across generation resets.
func removeStaleMemDiffs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		// Match "memfile.{uuid}" but not "memfile", "memfile.header", or "memfile.raw".
		if strings.HasPrefix(name, "memfile.") && name != snapshot.MemHeaderName && name != "memfile.raw" {
			os.Remove(filepath.Join(dir, name))
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
