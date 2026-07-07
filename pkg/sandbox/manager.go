package sandbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/devicemapper"
	"git.omukk.dev/wrenn/wrenn/pkg/envdclient"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/layout"
	"git.omukk.dev/wrenn/wrenn/pkg/models"
	"git.omukk.dev/wrenn/wrenn/pkg/network"
	"git.omukk.dev/wrenn/wrenn/pkg/vm"
	envdpb "git.omukk.dev/wrenn/wrenn/proto/envd/gen"
)

// Sentinel errors. Use errors.Is to detect them rather than string-matching.
var (
	// ErrNotFound is returned when a sandbox is not present in the in-memory map.
	ErrNotFound = errors.New("sandbox not found")
	// ErrNotRunning is returned when an operation requires StatusRunning but
	// the sandbox is in another state (or its envd client has been cleared
	// concurrently by a pause).
	ErrNotRunning = errors.New("sandbox not running")
	// ErrNotPaused is returned when an operation requires StatusPaused but
	// the sandbox is in another state.
	ErrNotPaused = errors.New("sandbox not paused")
	// ErrInvalidRange is returned when a metrics range parameter is invalid.
	ErrInvalidRange = errors.New("invalid range")
)

// MinTimeoutSec is the minimum inactivity TTL accepted by Create/Resume.
// 0 keeps the "no TTL" semantic; any positive value below this is clamped.
//
// Rationale: very short TTLs race the post-create/post-resume startup window
// (m.boxes insertion → /init → startMemoryLoader). With memLoadDone unset
// for a brief moment, the reaper guard does not fire and a sub-second
// TimeoutSec could auto-pause a sandbox before its memory loader arms,
// producing a stale ch.snapshot. 60s is well above the startup envelope.
const MinTimeoutSec = 60

// clampTimeout normalises a caller-supplied TTL. 0 means "no TTL" and is
// preserved; positive values are floored at MinTimeoutSec.
func clampTimeout(timeoutSec int) int {
	if timeoutSec <= 0 {
		return 0
	}
	if timeoutSec < MinTimeoutSec {
		return MinTimeoutSec
	}
	return timeoutSec
}

// envdReadyTimeoutFloor is the minimum time to wait for envd's /health to
// answer after a fresh boot or restore.
const envdReadyTimeoutFloor = 120 * time.Second

// envdReadyTimeoutPerGB scales the wait budget with guest RAM: larger VMs
// take longer to cold-boot (struct-page init, multi-vCPU bringup, cold
// dm-snapshot I/O).
const envdReadyTimeoutPerGB = 8 * time.Second

// envdReadyTimeout returns the WaitUntilReady deadline for a VM with the given
// memory size: 8s per GiB of RAM, floored at 120s. A 20 GiB guest gets 160s.
func envdReadyTimeout(memoryMB int) time.Duration {
	gb := (memoryMB + 1023) / 1024 // round up
	scaled := time.Duration(gb) * envdReadyTimeoutPerGB
	if scaled < envdReadyTimeoutFloor {
		return envdReadyTimeoutFloor
	}
	return scaled
}

// Config holds the paths and defaults for the sandbox manager.
type Config struct {
	WrennDir            string // root directory (e.g. /var/lib/wrenn); all sub-paths derived via layout package
	EnvdTimeout         time.Duration
	DefaultRootfsSizeMB int // target size for template rootfs images; 0 → DefaultDiskSizeMB

	// ProxyDomain is the public domain sandboxes are served under (e.g.
	// "wrenn.dev"). Injected into envd at /init so `envd ports` can build
	// {port}-{sandbox_id}.{domain} URLs.
	ProxyDomain string

	// Resolved at startup by the host agent.
	KernelPath    string // path to the latest vmlinux-x.y.z
	KernelVersion string // semver extracted from filename
	VMMBin        string // path to the cloud-hypervisor binary
	VMMVersion    string // semver from cloud-hypervisor --version
	AgentVersion  string // host agent version (injected via ldflags)

	// Activity sampler thresholds. The sampler polls each running sandbox's
	// guest liveness and refreshes its TTL when it is doing real work, so a
	// long-running but non-interactive job is not mistaken for inactive. A
	// sandbox counts as busy when guest CPU ≥ CPUBusyPct, or net/disk
	// throughput ≥ the respective floor (bytes/sec). Zero values fall back to
	// the package defaults at sampler start.
	ActivitySampleInterval time.Duration
	CPUBusyPct             float32
	NetFloorBps            uint64
	DiskFloorBps           uint64
}

// Activity sampler defaults. Thresholds sit clear of idle-VM background noise
// (envd's own sampler thread, guest timers) so a parked sandbox still times
// out; the debounce below guards against a lone noisy sample masquerading as
// work. All are env-overridable on the host agent.
const (
	defaultActivitySampleInterval = 5 * time.Second
	defaultCPUBusyPct             = 5.0       // percent of total vCPU capacity
	defaultNetFloorBps            = 16 * 1024 // 16 KB/s
	defaultDiskFloorBps           = 32 * 1024 // 32 KB/s
	activityPollTimeout           = 3 * time.Second
	activitySampleConcurrency     = 16
	// busyDebounceSamples is how many consecutive busy samples are required
	// before the sandbox's TTL is refreshed. With a 5s interval, real work
	// registers within ~10s while isolated noise spikes are ignored.
	busyDebounceSamples = 2
)

// LifecycleEvent describes an autonomous state change initiated by the agent.
type LifecycleEvent struct {
	Event     string
	SandboxID string
}

// EventSender sends autonomous lifecycle events to the control plane.
// SendAsync is fire-and-forget; Send blocks with retries and returns the
// final error so callers running under a shutdown deadline can guarantee
// delivery before process exit.
type EventSender interface {
	SendAsync(event LifecycleEvent)
	Send(ctx context.Context, event LifecycleEvent) error
}

// ErrDraining is returned by Create / Resume when the manager has begun
// shutdown. The agent process is about to pause every running sandbox and
// exit; admitting new lifecycle work would race the destroy loop and leave
// orphaned VMs after the process is gone.
var ErrDraining = errors.New("agent is draining for shutdown")

// Manager orchestrates sandbox lifecycle: VM, network, filesystem, envd.
type Manager struct {
	cfg    Config
	vm     *vm.Manager
	slots  *network.SlotAllocator
	loops  *devicemapper.LoopRegistry
	mu     sync.RWMutex
	boxes  map[string]*sandboxState
	stopCh chan struct{}
	// draining is set at the start of Shutdown. Create and Resume check it
	// (atomically, no lock needed) and refuse new work so the destroy loop
	// can run to completion without racing fresh RPCs.
	draining atomic.Bool

	// creates tracks in-flight Create calls by sandbox ID. An entry exists
	// only while Create is acquiring resources / booting the VM, before the
	// sandbox lands in boxes. Destroy consults it to abort a create that
	// would otherwise leak its half-built VM. Guarded by mu.
	creates map[string]*createHandle

	// onDestroy is called with the sandbox ID after cleanup completes.
	// Used by ProxyHandler to evict cached reverse proxies.
	onDestroy func(sandboxID string)

	// eventSender sends autonomous lifecycle events (auto-pause, auto-destroy)
	// to the CP via HTTP callback. Optional — nil means events are only
	// propagated through the HostMonitor reconciler.
	eventSender EventSender
}

// SetOnDestroy registers a callback invoked after each sandbox is cleaned up.
func (m *Manager) SetOnDestroy(fn func(sandboxID string)) {
	m.onDestroy = fn
}

// SetEventSender registers the callback sender for autonomous lifecycle events.
func (m *Manager) SetEventSender(sender EventSender) {
	m.eventSender = sender
}

// sandboxState holds the runtime state for a single sandbox.
type sandboxState struct {
	models.Sandbox
	lifecycleMu sync.Mutex // serializes Pause/Destroy/Resume on this sandbox
	slot        *network.Slot
	// client is published via atomic.Pointer so Exec/Pty/Process callers can
	// load it without holding lifecycleMu. Pause's releaseRuntime stores nil;
	// Resume stores a fresh client. Callers MUST nil-check after Load.
	client        atomic.Pointer[envdclient.Client]
	connTracker   *ConnTracker // tracks in-flight proxy connections for pre-pause drain
	dmDevice      *devicemapper.SnapshotDevice
	baseImagePath string // path to the base template rootfs (for loop registry release)

	// sandboxDirOverride, when non-empty, pins this sandbox's VMConfig.SandboxDir
	// to a path other than the default vm.SandboxTmpDir(sb.ID). Set when the
	// sandbox was launched from a snapshot template — CH's saved config.json
	// hardcodes the *original* source sandbox's tmpfs path, so every subsequent
	// restore (Resume, PauseAll/restart) must reuse that same path or CH cannot
	// find rootfs.ext4 in the new mount namespace.
	sandboxDirOverride string

	// lazyRestore records that this sandbox's CH process was launched with
	// --restore ...,memory_restore_mode=ondemand (Resume or snapshot-template
	// launch): guest pages fault in lazily from the source memory-ranges, so
	// any vm.snapshot taken before the guest-side preload completes would emit
	// holes for never-faulted pages — silent corruption on the next restore.
	// Pause/CreateSnapshot consult this via ensureMemoryMaterialized. Persisted
	// in the running-state file so re-attached sandboxes keep the guarantee.
	lazyRestore bool

	// Background memory loading state (set during Resume for UFFD sandboxes).
	// nil for freshly-created sandboxes. For resumed sandboxes, memLoadDone
	// is closed when the background loader finishes (success or failure).
	memLoadDone   chan struct{}      // closed when background memory loader exits
	memLoadCancel context.CancelFunc // cancels the background loader goroutine

	// Background zero-page punch state (set by Pause for paused sandboxes).
	// punchZeroPagesInDir runs off the pause critical path: the VM is already
	// destroyed and the snapshot dir swapped in, so the read-back-and-punch
	// pass need not block the user-perceived pause. Resume/Destroy cancel and
	// wait via waitForPunch before relaunching CH or removing the snapshot dir.
	punchDone   chan struct{}      // closed when the background punch exits
	punchCancel context.CancelFunc // cancels the background punch goroutine

	// Metrics sampling state.
	vmmPID        int                // VMM process PID (child of unshare wrapper)
	ring          *metricsRing       // tiered ring buffers for CPU/mem/disk metrics
	samplerCancel context.CancelFunc // cancels the per-sandbox sampling goroutine
	samplerDone   chan struct{}      // closed when the sampling goroutine exits

	// activityBusyStreak counts consecutive busy activity samples. A single
	// noisy sample (idle background CPU, a stray packet) must not refresh the
	// TTL, so LastActiveAt is only bumped once the streak reaches
	// busyDebounceSamples. Reset to 0 by any non-busy sample. Guarded by m.mu.
	activityBusyStreak int
}

// buildMetadata constructs the metadata map with version information.
func (m *Manager) buildMetadata(envdVersion string) map[string]string {
	meta := map[string]string{
		"kernel_version": m.cfg.KernelVersion,
		"vmm_version":    m.cfg.VMMVersion,
		"agent_version":  m.cfg.AgentVersion,
	}
	if envdVersion != "" {
		meta["envd_version"] = envdVersion
	}
	return meta
}

// createHandle coordinates an in-flight Create with a concurrent Destroy.
// cancel aborts the creation context; done is closed once Create has fully
// finished — whether it succeeded or rolled back a partial failure.
type createHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a new sandbox manager.
func New(cfg Config) *Manager {
	if cfg.EnvdTimeout == 0 {
		cfg.EnvdTimeout = 30 * time.Second
	}
	slots := network.NewSlotAllocator(layout.SlotsDir(cfg.WrennDir))
	// Honor slots claimed by other live processes (e.g. a concurrent CLI
	// invocation mid-Create) before handing any out ourselves.
	if err := slots.SeedFromDir(); err != nil {
		slog.Warn("failed to seed slot allocator from disk", "error", err)
	}
	return &Manager{
		cfg:     cfg,
		vm:      vm.NewManager(),
		slots:   slots,
		loops:   devicemapper.NewLoopRegistry(),
		boxes:   make(map[string]*sandboxState),
		creates: make(map[string]*createHandle),
		stopCh:  make(chan struct{}),
	}
}

// TemplateRootfsSize returns the actual disk usage of a template's rootfs
// file on this host. Uses block-level accounting (stat.Blocks * 512) so
// sparse files (even after EnsureImageSizes expansion) report only the
// blocks that are actually allocated on disk.
func (m *Manager) TemplateRootfsSize(teamID, templateID pgtype.UUID) (int64, error) {
	path := layout.TemplateRootfs(m.cfg.WrennDir, teamID, templateID)
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat template rootfs: %w", err)
	}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		return sys.Blocks * 512, nil
	}
	return info.Size(), nil
}

// Create boots a new sandbox. If the template's TemplateDir contains a CH
// memory snapshot (state.json + config.json) it is restored via CH's
// --restore + UFFD lazy memory; otherwise a fresh boot from the flattened
// rootfs is performed. defaultUser/defaultEnv are forwarded to envd's /init
// in both paths.
//
// If sandboxID is empty, a new ID is generated.
func (m *Manager) Create(
	ctx context.Context,
	sandboxID string,
	teamID, templateID pgtype.UUID,
	vcpus, memoryMB, timeoutSec, diskSizeMB int,
	defaultUser string,
	defaultEnv map[string]string,
) (*models.Sandbox, int64, error) {
	if m.draining.Load() {
		return nil, 0, ErrDraining
	}
	if sandboxID == "" {
		sandboxID = id.FormatSandboxID(id.NewSandboxID())
	}

	if vcpus <= 0 {
		vcpus = 2
	}
	if memoryMB <= 0 {
		memoryMB = 2048
	}
	if diskSizeMB <= 0 {
		diskSizeMB = m.cfg.DefaultRootfsSizeMB
	}
	timeoutSec = clampTimeout(timeoutSec)

	// Register an in-flight create handle before acquiring any resources so a
	// concurrent Destroy can abort this creation and wait for its rollback.
	// Without this, a Destroy that arrives while the VM is still booting finds
	// nothing in m.boxes, no-ops, and Create races on to register a VM that no
	// caller owns — a permanent VM / dm / network / loop leak.
	createCtx, cancelCreate := context.WithCancel(ctx)
	handle := &createHandle{cancel: cancelCreate, done: make(chan struct{})}
	m.mu.Lock()
	if _, exists := m.boxes[sandboxID]; exists {
		m.mu.Unlock()
		cancelCreate()
		return nil, 0, fmt.Errorf("sandbox %s already exists", sandboxID)
	}
	if _, inflight := m.creates[sandboxID]; inflight {
		m.mu.Unlock()
		cancelCreate()
		return nil, 0, fmt.Errorf("sandbox %s create already in progress", sandboxID)
	}
	m.creates[sandboxID] = handle
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.creates, sandboxID)
		m.mu.Unlock()
		cancelCreate()
		close(handle.done)
	}()
	// All subsequent steps run under the cancellable create context so a
	// concurrent Destroy can interrupt a slow VM boot / envd readiness wait.
	ctx = createCtx

	// Snapshot template? Route to the CH-restore path; the launcher manages
	// its own resource lifecycle and registers the sandbox itself.
	//
	// System base templates never carry a memory snapshot; guarding here
	// prevents a stray state.json (e.g. from a failed CreateSnapshot that
	// mis-targeted a base template) from silently rerouting fresh boots into
	// the restore path with a confusing error downstream.
	templateDir := layout.TemplateDir(m.cfg.WrennDir, teamID, templateID)
	if !layout.IsSystemTemplate(teamID, templateID) && layout.IsSnapshotTemplate(templateDir) {
		return m.createFromSnapshotTemplate(ctx, sandboxID, teamID, templateID,
			vcpus, memoryMB, timeoutSec, diskSizeMB, defaultUser, defaultEnv)
	}

	// Resolve base rootfs image.
	baseRootfs := layout.TemplateRootfs(m.cfg.WrennDir, teamID, templateID)
	if _, err := os.Stat(baseRootfs); err != nil {
		return nil, 0, fmt.Errorf("base rootfs not found at %s: %w", baseRootfs, err)
	}

	// Acquire shared read-only loop device for the base image.
	originLoop, err := m.loops.Acquire(baseRootfs)
	if err != nil {
		return nil, 0, fmt.Errorf("acquire loop device: %w", err)
	}

	originSize, err := devicemapper.OriginSizeBytes(originLoop)
	if err != nil {
		m.loops.Release(baseRootfs)
		return nil, 0, fmt.Errorf("get origin size: %w", err)
	}

	// Create dm-snapshot with per-sandbox CoW file.
	// CoW must be at least as large as the origin — if every block is
	// rewritten, the CoW stores a full copy. Undersized CoW causes
	// dm-snapshot invalidation → EIO on all guest I/O.
	dmName := "wrenn-" + sandboxID
	if err := os.MkdirAll(layout.SandboxDir(m.cfg.WrennDir, sandboxID), 0o755); err != nil {
		m.loops.Release(baseRootfs)
		return nil, 0, fmt.Errorf("create sandbox dir: %w", err)
	}
	cowPath := layout.SandboxCowPath(m.cfg.WrennDir, sandboxID)
	cowSize := max(int64(diskSizeMB)*1024*1024, originSize)
	dmDev, err := devicemapper.CreateSnapshot(dmName, originLoop, cowPath, originSize, cowSize)
	if err != nil {
		m.loops.Release(baseRootfs)
		return nil, 0, fmt.Errorf("create dm-snapshot: %w", err)
	}

	res := &createResources{
		sandboxID: sandboxID,
		loops:     m.loops,
		loopImage: baseRootfs,
		dmDevice:  dmDev,
		cowPath:   cowPath,
		slots:     m.slots,
	}

	// Allocate network slot.
	slotIdx, err := m.slots.Allocate()
	if err != nil {
		res.rollback()
		return nil, 0, fmt.Errorf("allocate network slot: %w", err)
	}
	res.slotIdx = slotIdx
	slot := network.NewSlot(slotIdx)

	// Set up network.
	if err := network.CreateNetwork(slot); err != nil {
		res.rollback()
		return nil, 0, fmt.Errorf("create network: %w", err)
	}
	res.slot = slot

	// Boot VM — CH gets the dm device path.
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
		VMMBin:           m.cfg.VMMBin,
		LogDir:           filepath.Join(m.cfg.WrennDir, "logs"),
	}

	if _, err := m.vm.Create(ctx, vmCfg); err != nil {
		res.rollback()
		return nil, 0, fmt.Errorf("create VM: %w", err)
	}
	res.vm = m.vm

	// Wait for envd to be ready. The budget scales with guest RAM — a large
	// VM cold-boots slower than the minimal default.
	client := envdclient.New(slot.HostIP.String())
	waitCtx, waitCancel := context.WithTimeout(ctx, envdReadyTimeout(memoryMB))
	defer waitCancel()

	if err := client.WaitUntilReady(waitCtx); err != nil {
		res.rollback()
		return nil, 0, fmt.Errorf("wait for envd: %w", err)
	}

	// Fetch envd version (best-effort).
	envdVersion, _ := client.FetchVersion(ctx)

	// Apply template defaults + sandbox identity via envd /init. Always called
	// on create so envd records its sandbox ID and proxy domain (used by
	// `envd ports`), even when the template specifies no user/env defaults.
	initCtx, initCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
	if err := client.PostInitWithDefaults(initCtx, defaultUser, defaultEnv, sandboxID, id.UUIDString(templateID), m.cfg.ProxyDomain); err != nil {
		slog.Warn("post-create PostInit failed", "id", sandboxID, "error", err)
	}
	initCancel()

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
		connTracker:   &ConnTracker{},
		dmDevice:      dmDev,
		baseImagePath: baseRootfs,
	}
	sb.client.Store(client)

	m.mu.Lock()
	m.boxes[sandboxID] = sb
	m.mu.Unlock()

	m.startSampler(sb)
	m.startCrashWatcher(sb)
	m.writeRunningState(sb)

	slog.Info("sandbox created",
		"id", sandboxID,
		"team_id", teamID,
		"template_id", templateID,
		"host_ip", slot.HostIP.String(),
		"dm_device", dmDev.DevicePath,
	)

	return &sb.Sandbox, cowSize, nil
}

// Destroy stops and cleans up a sandbox. If the sandbox is running, its VM,
// network, and rootfs are torn down. Any pause snapshot files are also removed.
func (m *Manager) Destroy(ctx context.Context, sandboxID string) error {
	m.mu.Lock()
	if handle, inflight := m.creates[sandboxID]; inflight {
		// A create is still in flight. Cancel it and wait for its rollback to
		// finish, otherwise the half-built VM / dm-snapshot / network / loop
		// device it acquired would leak with no owner. If the create instead
		// raced to success, it will have registered the sandbox in m.boxes by
		// the time done is closed — the normal teardown below then runs.
		m.mu.Unlock()
		slog.Info("destroy: aborting in-flight sandbox create", "id", sandboxID)
		handle.cancel()
		<-handle.done
		m.mu.Lock()
	}
	sb, ok := m.boxes[sandboxID]
	// statusAtEntry distinguishes "user is destroying an already-paused
	// sandbox" (legitimate cleanup → fall through) from "user is destroying
	// a running sandbox that raced to Paused before we got lifecycleMu"
	// (preserve snapshot → re-insert and bail). Captured under m.mu so it
	// reflects the same generation as the boxes-map delete.
	var statusAtEntry models.SandboxStatus
	if ok {
		statusAtEntry = sb.Status
		delete(m.boxes, sandboxID)
	}
	m.mu.Unlock()

	if ok {
		// Wait for any in-progress Pause to finish before tearing down resources.
		sb.lifecycleMu.Lock()
		defer sb.lifecycleMu.Unlock()

		// Racing-Pause guard. Only fires when the sandbox was NOT paused at
		// entry but became paused while we waited for lifecycleMu — i.e. a
		// concurrent Pause completed under us. In that case the snapshot was
		// just written to disk and destroying now would wipe a freshly-paused
		// sandbox. Re-insert into m.boxes (releaseRuntime already cleared
		// runtime refs; slot reservation retained for Resume) and return nil
		// so the agent's view stays consistent with the on-disk state.
		//
		// A legitimate Destroy of an already-paused sandbox (statusAtEntry ==
		// Paused) falls through to cleanup, which releases the slot and
		// removes the snapshot dir — the user explicitly asked for deletion.
		if statusAtEntry != models.StatusPaused && sb.Status == models.StatusPaused {
			m.mu.Lock()
			m.boxes[sandboxID] = sb
			m.mu.Unlock()
			slog.Info("destroy: racing pause completed, preserving snapshot", "id", sandboxID)
			return nil
		}
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
	// Stop any background zero-page punch before removing the snapshot dir,
	// so a lingering goroutine can't keep writing to files we're deleting.
	m.waitForPunch(sb)
	if sb.memLoadCancel != nil {
		sb.memLoadCancel()
		if sb.memLoadDone != nil {
			<-sb.memLoadDone
		}
	}
	m.stopSampler(sb)
	if err := m.vm.Destroy(ctx, sb.ID); err != nil {
		slog.Warn("vm destroy error", "id", sb.ID, "error", err)
	}
	if err := network.RemoveNetwork(sb.slot); err != nil {
		slog.Warn("network cleanup error", "id", sb.ID, "error", err)
	}
	m.slots.Release(sb.SlotIndex)

	// Tear down dm-snapshot and release the base image loop device.
	// RemoveSnapshot detaches the CoW loop even on failure, so deleting the
	// CoW file below cannot orphan it.
	if sb.dmDevice != nil {
		if err := devicemapper.RemoveSnapshot(context.Background(), sb.dmDevice); err != nil {
			slog.Warn("dm-snapshot remove error", "id", sb.ID, "error", err)
		}
		os.Remove(sb.dmDevice.CowPath)
	}
	// Paused branch: dm-snapshot and loop were already released by
	// releaseRuntime, which also cleared dmDevice and baseImagePath — both
	// guards below are nil/empty for a paused sandbox, so nothing double-
	// releases. The CoW file inside the sandbox dir is removed by Destroy's
	// os.RemoveAll(SandboxDir) below.
	if sb.baseImagePath != "" {
		m.loops.Release(sb.baseImagePath)
	}
}

// Pause, Resume, CreateSnapshot, FlattenRootfs, DeleteSnapshot, PauseAll
// are implemented in pause.go.

// activeClient resolves sandboxID to its envd client when the sandbox is in
// StatusRunning and the client has not been cleared by a concurrent pause.
// It bumps LastActiveAt as a side effect. Returns ErrNotFound if missing or
// ErrNotRunning (wrapped with context) otherwise.
//
// All Exec/Pty/Process methods funnel through this — it is the single
// chokepoint that guarantees we never deref a stale sb.client.
func (m *Manager) activeClient(sandboxID string) (*envdclient.Client, error) {
	sb, err := m.get(sandboxID)
	if err != nil {
		return nil, err
	}
	if sb.Status != models.StatusRunning {
		return nil, fmt.Errorf("%w: %s (status: %s)", ErrNotRunning, sandboxID, sb.Status)
	}
	c := sb.client.Load()
	if c == nil {
		// Race: status flipped from Running between m.get and Load (pause's
		// releaseRuntime cleared the pointer).
		return nil, fmt.Errorf("%w: %s (client cleared)", ErrNotRunning, sandboxID)
	}
	m.mu.Lock()
	sb.LastActiveAt = time.Now()
	m.mu.Unlock()
	return c, nil
}

// Exec runs a command inside a sandbox.
func (m *Manager) Exec(ctx context.Context, sandboxID string, cmd string, args []string, opts *envdclient.ExecOpts) (*envdclient.ExecResult, error) {
	c, err := m.activeClient(sandboxID)
	if err != nil {
		return nil, err
	}
	return c.Exec(ctx, cmd, args, opts)
}

// ExecStream runs a command inside a sandbox and returns a channel of streaming events.
func (m *Manager) ExecStream(ctx context.Context, sandboxID string, cmd string, args ...string) (<-chan envdclient.ExecStreamEvent, error) {
	c, err := m.activeClient(sandboxID)
	if err != nil {
		return nil, err
	}
	return c.ExecStream(ctx, cmd, args...)
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

// GetClient returns the envd client for a sandbox without bumping
// LastActiveAt. Used by the proxy path which has its own activity bookkeeping.
func (m *Manager) GetClient(sandboxID string) (*envdclient.Client, error) {
	sb, err := m.get(sandboxID)
	if err != nil {
		return nil, err
	}
	if sb.Status != models.StatusRunning {
		return nil, fmt.Errorf("%w: %s (status: %s)", ErrNotRunning, sandboxID, sb.Status)
	}
	c := sb.client.Load()
	if c == nil {
		return nil, fmt.Errorf("%w: %s (client cleared)", ErrNotRunning, sandboxID)
	}
	return c, nil
}

// SetDefaults calls envd's PostInit to configure the default user and
// environment variables for a running sandbox. This is called by the host
// agent after sandbox creation or resume when the template specifies defaults.
func (m *Manager) SetDefaults(ctx context.Context, sandboxID, defaultUser string, defaultEnv map[string]string) error {
	if defaultUser == "" && len(defaultEnv) == 0 {
		return nil
	}
	c, err := m.activeClient(sandboxID)
	if err != nil {
		return err
	}
	return c.PostInitWithDefaults(ctx, defaultUser, defaultEnv, "", "", "")
}

// PtyAttach starts a new PTY process or reconnects to an existing one.
// When reconnect is true it reattaches to the process identified by tag;
// otherwise it starts a new process (cmd may be empty to launch the user's
// default login shell). user empty means the sandbox default user.
func (m *Manager) PtyAttach(ctx context.Context, sandboxID, tag, cmd string, args []string, cols, rows uint32, envs map[string]string, cwd, user string, reconnect bool) (<-chan envdclient.PtyEvent, error) {
	c, err := m.activeClient(sandboxID)
	if err != nil {
		return nil, err
	}
	if reconnect {
		return c.PtyConnect(ctx, tag)
	}
	return c.PtyStart(ctx, tag, cmd, args, cols, rows, envs, cwd, user)
}

// PtySendInput sends raw bytes to a PTY process in a sandbox.
func (m *Manager) PtySendInput(ctx context.Context, sandboxID, tag string, data []byte) error {
	c, err := m.activeClient(sandboxID)
	if err != nil {
		return err
	}
	return c.PtySendInput(ctx, tag, data)
}

// PtyResize updates the terminal dimensions for a PTY process in a sandbox.
func (m *Manager) PtyResize(ctx context.Context, sandboxID, tag string, cols, rows uint32) error {
	c, err := m.activeClient(sandboxID)
	if err != nil {
		return err
	}
	return c.PtyResize(ctx, tag, cols, rows)
}

// PtyKill sends SIGKILL to a PTY process in a sandbox.
func (m *Manager) PtyKill(ctx context.Context, sandboxID, tag string) error {
	c, err := m.activeClient(sandboxID)
	if err != nil {
		return err
	}
	return c.PtyKill(ctx, tag)
}

// StartBackground starts a background process inside a sandbox.
func (m *Manager) StartBackground(ctx context.Context, sandboxID, tag, cmd string, args []string, envs map[string]string, cwd string) (uint32, error) {
	c, err := m.activeClient(sandboxID)
	if err != nil {
		return 0, err
	}
	return c.StartBackground(ctx, tag, cmd, args, envs, cwd)
}

// ConnectProcess re-attaches to a running process inside a sandbox.
func (m *Manager) ConnectProcess(ctx context.Context, sandboxID string, pid uint32, tag string) (<-chan envdclient.ExecStreamEvent, error) {
	c, err := m.activeClient(sandboxID)
	if err != nil {
		return nil, err
	}
	return c.ConnectProcess(ctx, pid, tag)
}

// ListProcesses returns all running processes inside a sandbox.
func (m *Manager) ListProcesses(ctx context.Context, sandboxID string) ([]envdclient.ProcessInfo, error) {
	c, err := m.activeClient(sandboxID)
	if err != nil {
		return nil, err
	}
	return c.ListProcesses(ctx)
}

// KillProcess sends a signal to a process inside a sandbox.
func (m *Manager) KillProcess(ctx context.Context, sandboxID string, pid uint32, tag string, signal envdpb.Signal) error {
	c, err := m.activeClient(sandboxID)
	if err != nil {
		return err
	}
	return c.KillProcess(ctx, pid, tag, signal)
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
	// Inbound proxy traffic counts as activity: an idle web server reachable
	// through the proxy should not be auto-paused while it is serving requests.
	m.mu.Lock()
	sb.LastActiveAt = time.Now()
	m.mu.Unlock()
	return sb.HostIP, sb.connTracker, true
}

// Ping resets the inactivity timer for a running sandbox.
func (m *Manager) Ping(sandboxID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sb, ok := m.boxes[sandboxID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, sandboxID)
	}
	if sb.Status != models.StatusRunning {
		return fmt.Errorf("%w: %s (status: %s)", ErrNotRunning, sandboxID, sb.Status)
	}
	sb.LastActiveAt = time.Now()
	return nil
}

// DrainAutoPausedIDs returns IDs that auto-paused since the last drain.
// The autonomous pause paths (TTL reaper, PauseAll on shutdown / heartbeat
// failure) emit per-sandbox events through eventSender directly, so this
// list is currently unused. Retained for proto compatibility.
func (m *Manager) DrainAutoPausedIDs() []string {
	return nil
}

func (m *Manager) get(sandboxID string) (*sandboxState, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sb, ok := m.boxes[sandboxID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, sandboxID)
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
		// Skip sandboxes still loading memory — they're initializing.
		if sb.memLoadDone != nil {
			select {
			case <-sb.memLoadDone:
			default:
				continue
			}
		}
		if now.Sub(sb.LastActiveAt) > time.Duration(sb.TimeoutSec)*time.Second {
			expired = append(expired, id)
		}
	}
	m.mu.RUnlock()

	for _, id := range expired {
		slog.Info("TTL expired, auto-pausing sandbox", "id", id)
		pauseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err := m.Pause(pauseCtx, id)
		cancel()
		if err != nil {
			slog.Warn("TTL auto-pause failed, destroying sandbox", "id", id, "error", err)
			if destroyErr := m.Destroy(context.Background(), id); destroyErr != nil {
				slog.Warn("TTL destroy after failed pause also failed", "id", id, "error", destroyErr)
			} else if m.eventSender != nil {
				m.eventSender.SendAsync(LifecycleEvent{
					Event:     "sandbox.stopped",
					SandboxID: id,
				})
			}
			continue
		}

		if m.eventSender != nil {
			m.eventSender.SendAsync(LifecycleEvent{
				Event:     "sandbox.auto_paused",
				SandboxID: id,
			})
		}
	}
}

// StartActivitySampler starts a background goroutine that polls each running
// sandbox's guest liveness (CPU + net/disk IO) and refreshes LastActiveAt when
// the sandbox is doing real work. This is what keeps a long-running but
// non-interactive job (a build, a download) from being auto-paused by the TTL
// reaper, while an idle workload (sleep, a parked shell) still times out.
func (m *Manager) StartActivitySampler(ctx context.Context) {
	interval := m.cfg.ActivitySampleInterval
	if interval <= 0 {
		interval = defaultActivitySampleInterval
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.sampleActivity(ctx)
			}
		}
	}()
}

// activityTarget pairs a sandbox ID with the envd client to poll.
type activityTarget struct {
	id     string
	client *envdclient.Client
}

func (m *Manager) sampleActivity(ctx context.Context) {
	// Snapshot the running sandboxes and their clients under the lock, then
	// poll over the network without holding it.
	m.mu.RLock()
	targets := make([]activityTarget, 0, len(m.boxes))
	for id, sb := range m.boxes {
		if sb.Status != models.StatusRunning {
			continue
		}
		// Skip sandboxes still loading memory after a resume — they are not
		// settled and their IO/CPU is preload noise, not user work.
		if sb.memLoadDone != nil {
			select {
			case <-sb.memLoadDone:
			default:
				continue
			}
		}
		c := sb.client.Load()
		if c == nil {
			continue
		}
		targets = append(targets, activityTarget{id: id, client: c})
	}
	m.mu.RUnlock()

	if len(targets) == 0 {
		return
	}

	sem := make(chan struct{}, activitySampleConcurrency)
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(t activityTarget) {
			defer wg.Done()
			defer func() { <-sem }()
			m.pollAndBump(ctx, t)
		}(t)
	}
	wg.Wait()
}

// pollAndBump fetches one sandbox's activity and refreshes its TTL once it has
// been busy for busyDebounceSamples consecutive samples. Poll failures are
// treated as a non-busy sample: an unreachable envd is handled by the reaper /
// heartbeat paths, and resetting the streak is the safe default.
func (m *Manager) pollAndBump(ctx context.Context, t activityTarget) {
	pollCtx, cancel := context.WithTimeout(ctx, activityPollTimeout)
	defer cancel()

	act, err := t.client.FetchActivity(pollCtx)
	busy := err == nil && m.isBusy(act)

	m.mu.Lock()
	defer m.mu.Unlock()

	sb, ok := m.boxes[t.id]
	if !ok || sb.Status != models.StatusRunning {
		return
	}

	streak, bump := applyBusySample(sb.activityBusyStreak, busy)
	sb.activityBusyStreak = streak
	if bump {
		sb.LastActiveAt = time.Now()
	}
}

// applyBusySample advances a debounce streak with the latest sample and
// reports whether the TTL should be refreshed this tick. A non-busy sample
// resets the streak; the bump fires once the streak reaches the debounce
// threshold and on every busy tick thereafter (the streak is held at the
// threshold rather than growing unbounded).
func applyBusySample(streak int, busy bool) (newStreak int, bump bool) {
	if !busy {
		return 0, false
	}
	streak++
	if streak >= busyDebounceSamples {
		return busyDebounceSamples, true
	}
	return streak, false
}

// isBusy reports whether a guest liveness snapshot represents real work.
func (m *Manager) isBusy(act *envdclient.Activity) bool {
	cpuThreshold := m.cfg.CPUBusyPct
	if cpuThreshold <= 0 {
		cpuThreshold = defaultCPUBusyPct
	}
	netFloor := m.cfg.NetFloorBps
	if netFloor == 0 {
		netFloor = defaultNetFloorBps
	}
	diskFloor := m.cfg.DiskFloorBps
	if diskFloor == 0 {
		diskFloor = defaultDiskFloorBps
	}

	return act.CPUUsedPct >= cpuThreshold ||
		act.NetBps >= netFloor ||
		act.DiskBps >= diskFloor
}

// Shutdown gracefully drains the manager. Running sandboxes are paused so
// their state survives across agent restarts; any sandboxes still holding
// runtime resources after PauseAll (e.g. paused failed, or status was
// Starting/Resuming/Error) are destroyed to release VM / dm / loop / netns.
// Finally the shared loop registry is fully released.
func (m *Manager) Shutdown(ctx context.Context) {
	// Flip draining BEFORE close(stopCh) so any Create/Resume already inside
	// its handler-goroutine sees the flag on its next check. Subsequent RPC
	// handlers that load the flag get ErrDraining and return immediately.
	m.draining.Store(true)
	close(m.stopCh)

	// Cancel in-flight Create calls and wait for them to settle. A slow create
	// (envd readiness wait scales up to ~160s for large VMs) would otherwise
	// register its VM in m.boxes after the destroy loop below has run, leaking
	// it. After the wait each create has either rolled back or registered in
	// m.boxes — where PauseAll / the destroy loop pick it up.
	m.mu.Lock()
	inflight := make([]*createHandle, 0, len(m.creates))
	for _, h := range m.creates {
		h.cancel()
		inflight = append(inflight, h)
	}
	m.mu.Unlock()
	for _, h := range inflight {
		<-h.done
	}

	// Snapshot every running sandbox. PauseAll calls Pause per-sandbox which
	// internally calls releaseRuntime → frees VM, network, dm-snapshot, and
	// the base-image loop refcount.
	slog.Info("shutdown: pausing running sandboxes")
	m.PauseAll(ctx)

	// Destroy anything still holding runtime resources. A Paused sandbox has
	// already had releaseRuntime called, so re-destroying it is harmless but
	// also unnecessary — we destroy regardless to remove it from the boxes
	// map and to handle states where Pause failed or wasn't applicable.
	m.mu.RLock()
	ids := make([]string, 0, len(m.boxes))
	for id, sb := range m.boxes {
		// Paused sandboxes already had runtime freed by PauseAll. Leave the
		// snapshot dir on disk so the next agent instance can resume them.
		if sb.Status == models.StatusPaused {
			continue
		}
		ids = append(ids, id)
	}
	m.mu.RUnlock()

	for _, sbID := range ids {
		slog.Info("shutdown: destroying sandbox", "id", sbID)
		if err := m.Destroy(ctx, sbID); err != nil {
			slog.Warn("shutdown destroy failed", "id", sbID, "error", err)
			continue
		}
		// Notify CP so the DB row flips off running/pausing/error to stopped.
		// Async: a sync Send with CP unreachable can burn ~31s per sandbox
		// (3 × 10s HTTP timeout + backoff) and blow the 5min shutdown budget.
		// Best-effort — if the agent process exits before the goroutine's
		// HTTP request lands, HostMonitor's missing-confirmed-dead reconcile
		// catches it after the next agent restart (it sees the sandbox in DB
		// as 'running'/'missing' but not present in ListSandboxes → stopped).
		if m.eventSender != nil {
			m.eventSender.SendAsync(LifecycleEvent{
				Event:     "sandbox.stopped",
				SandboxID: sbID,
			})
		}
	}

	m.loops.ReleaseAll()
}

// warnErr logs a warning if err is non-nil. Used for best-effort cleanup
// in error paths where the primary error has already been captured.
func warnErr(msg string, id string, err error) {
	if err != nil {
		slog.Warn(msg, "id", id, "error", err)
	}
}

// createResources tracks partially-acquired resources during sandbox creation
// so they can be rolled back in reverse order on failure.
type createResources struct {
	sandboxID string
	loops     *devicemapper.LoopRegistry
	vm        *vm.Manager
	loopImage string
	dmDevice  *devicemapper.SnapshotDevice
	cowPath   string
	slotIdx   int
	slots     *network.SlotAllocator
	slot      *network.Slot
	rollCow   func() // optional custom cow rollback (e.g. rename back)
}

func (r *createResources) rollback() {
	if r.vm != nil && r.sandboxID != "" {
		warnErr("vm destroy error", r.sandboxID, r.vm.Destroy(context.Background(), r.sandboxID))
	}
	if r.slot != nil {
		warnErr("network cleanup error", r.sandboxID, network.RemoveNetwork(r.slot))
	}
	if r.slots != nil && r.slotIdx > 0 {
		r.slots.Release(r.slotIdx)
	}
	if r.dmDevice != nil {
		warnErr("dm-snapshot remove error", r.sandboxID, devicemapper.RemoveSnapshot(context.Background(), r.dmDevice))
	}
	if r.rollCow != nil {
		r.rollCow()
	} else if r.cowPath != "" {
		os.Remove(r.cowPath)
	}
	if r.loopImage != "" {
		r.loops.Release(r.loopImage)
	}
}

// startCrashWatcher monitors the VM process for unexpected exits.
// If the process exits while the sandbox is still in m.boxes (i.e. not a
// deliberate Destroy), the sandbox is cleaned up and a sandbox.error event
// is pushed to the control plane.
func (m *Manager) startCrashWatcher(sb *sandboxState) {
	v, ok := m.vm.Get(sb.ID)
	if !ok {
		return
	}
	go func() {
		select {
		case <-v.Exited():
		case <-m.stopCh:
			return
		}

		// Check if this was a deliberate Destroy/Pause (sandbox already removed
		// from boxes, or Pause owns the cleanup). StatusPaused must also bail
		// because the crash watcher races with Pause flipping status to Paused
		// after vm.Destroy is called as part of releaseRuntime.
		m.mu.Lock()
		_, stillAlive := m.boxes[sb.ID]
		if stillAlive && (sb.Status == models.StatusPausing || sb.Status == models.StatusPaused) {
			stillAlive = false
		}
		if stillAlive {
			delete(m.boxes, sb.ID)
		}
		m.mu.Unlock()

		if !stillAlive {
			return
		}

		slog.Error("VM process crashed, cleaning up", "id", sb.ID)

		sb.lifecycleMu.Lock()
		m.cleanupAfterCrash(sb)
		sb.lifecycleMu.Unlock()

		if m.onDestroy != nil {
			m.onDestroy(sb.ID)
		}

		if m.eventSender != nil {
			m.eventSender.SendAsync(LifecycleEvent{
				Event:     "sandbox.error",
				SandboxID: sb.ID,
			})
		}
	}()
}

// cleanupAfterCrash tears down sandbox resources after a VM crash.
// The VM process is already dead so we skip vm.Destroy and just clean up
// network, device-mapper, and loop devices.
func (m *Manager) cleanupAfterCrash(sb *sandboxState) {
	if sb.memLoadCancel != nil {
		sb.memLoadCancel()
		if sb.memLoadDone != nil {
			<-sb.memLoadDone
		}
	}
	m.stopSampler(sb)

	// Remove the VM from the vm.Manager's map (process is already dead).
	_ = m.vm.Destroy(context.Background(), sb.ID)

	if err := network.RemoveNetwork(sb.slot); err != nil {
		slog.Warn("crash cleanup: network error", "id", sb.ID, "error", err)
	}
	m.slots.Release(sb.SlotIndex)

	// RemoveSnapshot detaches the CoW loop even on failure, so the RemoveAll
	// below (which deletes the CoW backing file) cannot orphan it.
	if sb.dmDevice != nil {
		if err := devicemapper.RemoveSnapshot(context.Background(), sb.dmDevice); err != nil {
			slog.Warn("crash cleanup: dm-snapshot error", "id", sb.ID, "error", err)
		}
	}
	if sb.baseImagePath != "" {
		m.loops.Release(sb.baseImagePath)
	}
	if err := os.RemoveAll(layout.SandboxDir(m.cfg.WrennDir, sb.ID)); err != nil {
		slog.Warn("crash cleanup: sandbox dir error", "id", sb.ID, "error", err)
	}
}

// startSampler resolves the VMM PID and starts a background goroutine
// that samples CPU/mem/disk at 1s intervals into the ring buffer.
// Must be called after the sandbox is registered in m.boxes.
func (m *Manager) startSampler(sb *sandboxState) {
	v, ok := m.vm.Get(sb.ID)
	if !ok {
		slog.Warn("metrics: VM not found, skipping sampler", "id", sb.ID)
		return
	}

	// v.PID() is the cmd.Process.Pid of the "unshare -m -- bash -c script"
	// invocation. The exec chain (unshare → bash → ip netns exec → cloud-hypervisor)
	// occupies the same PID. v.PID() IS the VMM PID.
	vmmPID := v.PID()

	sb.vmmPID = vmmPID
	sb.ring = newMetricsRing()

	ctx, cancel := context.WithCancel(context.Background())
	sb.samplerCancel = cancel
	sb.samplerDone = make(chan struct{})

	// Read initial CPU counters for delta calculation.
	// Passed to goroutine as local state — no shared mutation.
	initialCPU, err := readCPUStat(vmmPID)
	if err != nil {
		slog.Warn("metrics: could not read initial CPU stat", "id", sb.ID, "error", err)
	}

	go m.samplerLoop(ctx, sb, vmmPID, sb.VCPUs, initialCPU)
}

// samplerLoop samples metrics at 1s intervals.
// lastCPU is goroutine-local to avoid shared-state races.
func (m *Manager) samplerLoop(ctx context.Context, sb *sandboxState, vmmPID, vcpus int, lastCPU cpuStat) {
	defer close(sb.samplerDone)

	ticker := time.NewTicker(1 * time.Second)
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
			cur, err := readCPUStat(vmmPID)
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

			// Memory & disk: guest-reported metrics from envd /metrics.
			// Using the guest's own view for both is accurate and avoids
			// host-side CoW file quirks (sparse allocation, silent errors).
			var memBytes, diskBytes int64
			if m, err := readEnvdMetrics(ctx, sb.client.Load()); err == nil {
				memBytes = m.MemBytes
				diskBytes = m.DiskBytes
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
		return nil, fmt.Errorf("%w: %s", ErrNotFound, sandboxID)
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
		return nil, fmt.Errorf("%w: %s (valid: 5m, 10m, 1h, 2h, 6h, 12h, 24h)", ErrInvalidRange, rangeTier)
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
		return nil, nil, nil, fmt.Errorf("%w: %s", ErrNotFound, sandboxID)
	}

	m.stopSampler(sb)
	if sb.ring == nil {
		return nil, nil, nil, nil
	}
	pts10m, pts2h, pts24h = sb.ring.Flush()
	return pts10m, pts2h, pts24h, nil
}
