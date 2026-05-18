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
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/devicemapper"
	"git.omukk.dev/wrenn/wrenn/internal/envdclient"
	"git.omukk.dev/wrenn/wrenn/internal/layout"
	"git.omukk.dev/wrenn/wrenn/internal/models"
	"git.omukk.dev/wrenn/wrenn/internal/network"
	"git.omukk.dev/wrenn/wrenn/internal/vm"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	envdpb "git.omukk.dev/wrenn/wrenn/proto/envd/gen"
)

// ErrNotFound is returned when a sandbox is not present in the in-memory map.
var ErrNotFound = errors.New("sandbox not found")

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

// Config holds the paths and defaults for the sandbox manager.
type Config struct {
	WrennDir            string // root directory (e.g. /var/lib/wrenn); all sub-paths derived via layout package
	EnvdTimeout         time.Duration
	DefaultRootfsSizeMB int // target size for template rootfs images; 0 → DefaultDiskSizeMB

	// Resolved at startup by the host agent.
	KernelPath    string // path to the latest vmlinux-x.y.z
	KernelVersion string // semver extracted from filename
	VMMBin        string // path to the cloud-hypervisor binary
	VMMVersion    string // semver from cloud-hypervisor --version
	AgentVersion  string // host agent version (injected via ldflags)
}

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

// Manager orchestrates sandbox lifecycle: VM, network, filesystem, envd.
type Manager struct {
	cfg    Config
	vm     *vm.Manager
	slots  *network.SlotAllocator
	loops  *devicemapper.LoopRegistry
	mu     sync.RWMutex
	boxes  map[string]*sandboxState
	stopCh chan struct{}

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
	lifecycleMu   sync.Mutex // serializes Pause/Destroy/Resume on this sandbox
	slot          *network.Slot
	client        *envdclient.Client
	connTracker   *ConnTracker // tracks in-flight proxy connections for pre-pause drain
	dmDevice      *devicemapper.SnapshotDevice
	baseImagePath string // path to the base template rootfs (for loop registry release)

	// Background memory loading state (set during Resume for UFFD sandboxes).
	// nil for freshly-created sandboxes. For resumed sandboxes, memLoadDone
	// is closed when the background loader finishes (success or failure).
	memLoadDone   chan struct{}      // closed when background memory loader exits
	memLoadCancel context.CancelFunc // cancels the background loader goroutine

	// Metrics sampling state.
	vmmPID        int                // VMM process PID (child of unshare wrapper)
	ring          *metricsRing       // tiered ring buffers for CPU/mem/disk metrics
	samplerCancel context.CancelFunc // cancels the per-sandbox sampling goroutine
	samplerDone   chan struct{}      // closed when the sampling goroutine exits
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
) (*models.Sandbox, error) {
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
	timeoutSec = clampTimeout(timeoutSec)

	// Snapshot template? Route to the CH-restore path; the launcher manages
	// its own resource lifecycle and registers the sandbox itself.
	//
	// The minimal base template never carries a memory snapshot; guarding
	// here prevents a stray state.json (e.g. from a failed CreateSnapshot
	// that mis-targeted minimal) from silently rerouting fresh boots into
	// the restore path with a confusing error downstream.
	templateDir := layout.TemplateDir(m.cfg.WrennDir, teamID, templateID)
	if !layout.IsMinimal(teamID, templateID) && layout.IsSnapshotTemplate(templateDir) {
		return m.createFromSnapshotTemplate(ctx, sandboxID, teamID, templateID,
			vcpus, memoryMB, timeoutSec, diskSizeMB, defaultUser, defaultEnv)
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
	// CoW must be at least as large as the origin — if every block is
	// rewritten, the CoW stores a full copy. Undersized CoW causes
	// dm-snapshot invalidation → EIO on all guest I/O.
	dmName := "wrenn-" + sandboxID
	cowPath := filepath.Join(layout.SandboxesDir(m.cfg.WrennDir), fmt.Sprintf("%s.cow", sandboxID))
	cowSize := max(int64(diskSizeMB)*1024*1024, originSize)
	dmDev, err := devicemapper.CreateSnapshot(dmName, originLoop, cowPath, originSize, cowSize)
	if err != nil {
		m.loops.Release(baseRootfs)
		return nil, fmt.Errorf("create dm-snapshot: %w", err)
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
		return nil, fmt.Errorf("allocate network slot: %w", err)
	}
	res.slotIdx = slotIdx
	slot := network.NewSlot(slotIdx)

	// Set up network.
	if err := network.CreateNetwork(slot); err != nil {
		res.rollback()
		return nil, fmt.Errorf("create network: %w", err)
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
		return nil, fmt.Errorf("create VM: %w", err)
	}
	res.vm = m.vm

	// Wait for envd to be ready.
	client := envdclient.New(slot.HostIP.String())
	waitCtx, waitCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
	defer waitCancel()

	if err := client.WaitUntilReady(waitCtx); err != nil {
		res.rollback()
		return nil, fmt.Errorf("wait for envd: %w", err)
	}

	// Fetch envd version (best-effort).
	envdVersion, _ := client.FetchVersion(ctx)

	// Apply template defaults via envd /init (no-op when both empty).
	if defaultUser != "" || len(defaultEnv) > 0 {
		initCtx, initCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
		if err := client.PostInitWithDefaults(initCtx, defaultUser, defaultEnv, sandboxID, id.UUIDString(templateID)); err != nil {
			slog.Warn("post-create PostInit failed", "id", sandboxID, "error", err)
		}
		initCancel()
	}

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
	m.startCrashWatcher(sb)

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
	if sb.dmDevice != nil {
		if err := devicemapper.RemoveSnapshot(context.Background(), sb.dmDevice); err != nil {
			slog.Warn("dm-snapshot remove error", "id", sb.ID, "error", err)
		}
		os.Remove(sb.dmDevice.CowPath)
	} else {
		// Paused sandbox: dm-snapshot and loop were already released by
		// releaseRuntime, but the CoW file at sandboxes/{id}.cow persisted
		// so Resume could re-attach. On Destroy that file would leak —
		// derive its path from the sandbox ID and remove it.
		cowPath := filepath.Join(layout.SandboxesDir(m.cfg.WrennDir), sb.ID+".cow")
		os.Remove(cowPath)
	}
	if sb.baseImagePath != "" {
		m.loops.Release(sb.baseImagePath)
	}
}

// Pause, Resume, CreateSnapshot, FlattenRootfs, DeleteSnapshot, PauseAll
// are implemented in pause.go.

// Exec runs a command inside a sandbox.
func (m *Manager) Exec(ctx context.Context, sandboxID string, cmd string, args []string, opts *envdclient.ExecOpts) (*envdclient.ExecResult, error) {
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

	return sb.client.Exec(ctx, cmd, args, opts)
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
	return sb.client.PostInitWithDefaults(ctx, defaultUser, defaultEnv, "", "")
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
		return fmt.Errorf("%w: %s", ErrNotFound, sandboxID)
	}
	if sb.Status != models.StatusRunning {
		return fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
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

// Shutdown gracefully drains the manager. Running sandboxes are paused so
// their state survives across agent restarts; any sandboxes still holding
// runtime resources after PauseAll (e.g. paused failed, or status was
// Starting/Resuming/Error) are destroyed to release VM / dm / loop / netns.
// Finally the shared loop registry is fully released.
func (m *Manager) Shutdown(ctx context.Context) {
	close(m.stopCh)

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

	if sb.dmDevice != nil {
		if err := devicemapper.RemoveSnapshot(context.Background(), sb.dmDevice); err != nil {
			slog.Warn("crash cleanup: dm-snapshot error", "id", sb.ID, "error", err)
		}
		os.Remove(sb.dmDevice.CowPath)
	}
	if sb.baseImagePath != "" {
		m.loops.Release(sb.baseImagePath)
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

			// Memory: guest-reported used memory from envd /metrics.
			// VmRSS of the VMM process includes guest page cache and never
			// decreases, so we use the guest's own view which reports
			// total - available (actual process memory).
			memBytes, _ := readEnvdMemUsed(ctx, sb.client)

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
		return nil, nil, nil, fmt.Errorf("%w: %s", ErrNotFound, sandboxID)
	}

	m.stopSampler(sb)
	if sb.ring == nil {
		return nil, nil, nil, nil
	}
	pts10m, pts2h, pts24h = sb.ring.Flush()
	return pts10m, pts2h, pts24h, nil
}
