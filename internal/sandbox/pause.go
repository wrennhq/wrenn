// Package sandbox: pause / resume / live-snapshot orchestration.
//
// Two high-level operations both built on the same CH primitives. Names use
// wrenn.* vs ch.* so it is clear which layer a step belongs to.
//
//	wrenn.snapshot  =  ch.pause + ch.snapshot + ch.resume
//	                   artefacts -> WRENN_DIR/images/teams/{teamID}/{templateID}/
//	                   sandbox keeps running; dm-snapshot also flattened into
//	                   rootfs.ext4 so the dir is a self-contained template.
//
//	wrenn.pause     =  ch.pause + ch.snapshot + ch.destroy
//	                   artefacts -> WRENN_DIR/sandboxes/{sandboxID}/
//	                   VM torn down; CoW file at WRENN_DIR/sandboxes/{id}.cow
//	                   + network slot retained so resume reaches the same host-IP.
//
// Pause always writes to a fresh staging directory and atomically swaps it
// into place after ch.destroy releases CH's open fd to the previous
// generation's memory-ranges (held via userfaultfd for lazy memory restore).
// This is what makes pause-resume-pause-resume chains correct: an in-place
// rewrite would risk CH reading from the file we are simultaneously
// overwriting.
//
// CH 52+ writes memory-ranges as a sparse file via SEEK_DATA/SEEK_HOLE,
// combined with `thp:false` + `free_page_reporting:true` on the balloon and
// a pre-pause balloon inflation to reclaim guest free pages — no userspace
// hole punching needed.
package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/devicemapper"
	"git.omukk.dev/wrenn/wrenn/internal/envdclient"
	"git.omukk.dev/wrenn/wrenn/internal/layout"
	"git.omukk.dev/wrenn/wrenn/internal/models"
	"git.omukk.dev/wrenn/wrenn/internal/network"
	"git.omukk.dev/wrenn/wrenn/internal/snapshot"
	"git.omukk.dev/wrenn/wrenn/internal/vm"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

const (
	// snapshotMetaFile is the per-snapshot metadata file holding the info
	// needed to restore the sandbox (template, resources, slot, etc.).
	snapshotMetaFile = "wrenn-snapshot.json"

	// drainTimeout is how long pause waits for in-flight proxy connections
	// to release before forcibly cancelling them.
	drainTimeout = 5 * time.Second
)

// snapshotMeta is persisted into every snapshot directory. It captures the
// minimum information needed to restore the sandbox or build a new sandbox
// from a template, independent of the in-memory state in m.boxes.
type snapshotMeta struct {
	SandboxID    string    `json:"sandbox_id"`
	TeamID       string    `json:"team_id"`
	TemplateID   string    `json:"template_id"`
	VCPUs        int       `json:"vcpus"`
	MemoryMB     int       `json:"memory_mb"`
	TimeoutSec   int       `json:"timeout_sec"`
	SlotIndex    int       `json:"slot_index"`
	BaseTemplate string    `json:"base_template"`
	CowPath      string    `json:"cow_path,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func writeSnapshotMeta(dir string, m *snapshotMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot meta: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, snapshotMetaFile), data, 0o644); err != nil {
		return fmt.Errorf("write snapshot meta: %w", err)
	}
	return nil
}

func readSnapshotMeta(dir string) (*snapshotMeta, error) {
	data, err := os.ReadFile(filepath.Join(dir, snapshotMetaFile))
	if err != nil {
		return nil, fmt.Errorf("read snapshot meta: %w", err)
	}
	var meta snapshotMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot meta: %w", err)
	}
	return &meta, nil
}

// Pause freezes the VM, persists the snapshot to WRENN_DIR/sandboxes/{id}/,
// and tears down VM/network/dm resources. The CoW file is kept on disk so
// Resume can pick up where the sandbox left off.
//
// The sandbox stays in m.boxes with Status=Paused. The cow file at
// WRENN_DIR/sandboxes/{id}.cow persists; on Resume it is re-attached via
// devicemapper.RestoreSnapshot.
//
// Write strategy: snapshot is written into a fresh staging directory, the
// VM is destroyed (closing CH's open fd to any previous-generation
// memory-ranges), then the staging directory atomically replaces the
// previous one via rename. This is essential for pause-resume-pause chains
// where CH holds the old memory-ranges open via userfaultfd while we write
// the new one.
func (m *Manager) Pause(ctx context.Context, sandboxID string) error {
	sb, err := m.get(sandboxID)
	if err != nil {
		return err
	}

	sb.lifecycleMu.Lock()
	defer sb.lifecycleMu.Unlock()

	if sb.Status == models.StatusPaused {
		return nil
	}
	if sb.Status != models.StatusRunning {
		return fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	// Wait for the post-resume memory loader to finish before snapshotting.
	// Without this, ch.snapshot's SEEK_DATA/SEEK_HOLE writer would emit holes
	// for any page not yet faulted in, which read back as zero on the next
	// restore — silent corruption across pause/resume chains.
	if err := m.waitForMemoryLoader(ctx, sb); err != nil {
		return fmt.Errorf("pause %s: %w", sandboxID, err)
	}

	m.mu.Lock()
	sb.Status = models.StatusPausing
	m.mu.Unlock()

	finalDir := layout.PauseSnapshotDir(m.cfg.WrennDir, sandboxID)
	stageDir := layout.PauseStagingDir(m.cfg.WrennDir, sandboxID)

	rollbackToRunning := func(cause error, stage string) error {
		_ = os.RemoveAll(stageDir)
		// If the VM can't be unfrozen the sandbox is no longer usable.
		// Mark it Error so subsequent RPCs don't operate on a broken VM
		// (especially after a partial vm.snapshot which can leave CH wedged).
		if rerr := m.vm.Resume(context.Background(), sandboxID); rerr != nil {
			m.mu.Lock()
			sb.Status = models.StatusError
			m.mu.Unlock()
			sb.connTracker.Reset()
			return fmt.Errorf("pause %s: %s: %w (and resume failed: %v)",
				sandboxID, stage, cause, rerr)
		}
		sb.connTracker.Reset()
		m.mu.Lock()
		sb.Status = models.StatusRunning
		m.mu.Unlock()
		return fmt.Errorf("pause %s: %s: %w", sandboxID, stage, cause)
	}

	if err := m.quiesceAndPauseCH(ctx, sb); err != nil {
		return rollbackToRunning(err, "quiesce")
	}

	// Memory materialisation is handled out-of-band by the background loader
	// kicked off by Resume after /init. We blocked on it above (waitForMemoryLoader)
	// so by the time we reach ch.snapshot every guest page is resident in CH's
	// memfile and SEEK_DATA/SEEK_HOLE produces a self-contained snapshot.

	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return rollbackToRunning(err, "mkdir staging")
	}
	if err := m.vm.Snapshot(ctx, sandboxID, stageDir); err != nil {
		return rollbackToRunning(err, "snapshot")
	}

	meta := &snapshotMeta{
		SandboxID:    sb.ID,
		TeamID:       id.UUIDString(pgtype.UUID{Bytes: sb.TemplateTeamID, Valid: true}),
		TemplateID:   id.UUIDString(pgtype.UUID{Bytes: sb.TemplateID, Valid: true}),
		VCPUs:        sb.VCPUs,
		MemoryMB:     sb.MemoryMB,
		TimeoutSec:   sb.TimeoutSec,
		SlotIndex:    sb.SlotIndex,
		BaseTemplate: sb.baseImagePath,
		CowPath:      sb.dmDevice.CowPath,
		CreatedAt:    time.Now(),
	}
	if err := writeSnapshotMeta(stageDir, meta); err != nil {
		// Without meta, Resume cannot reconstruct the sandbox. Treat as fatal.
		_ = os.RemoveAll(stageDir)
		return rollbackToRunning(err, "write meta")
	}

	// releaseRuntime destroys the VM, which closes CH's open fd to any
	// previous-generation memory-ranges. Must happen BEFORE we touch finalDir
	// so the swap is safe.
	m.releaseRuntime(sb, keepCow)

	if err := swapDir(stageDir, finalDir); err != nil {
		// CH is already destroyed — we cannot roll back to Running. The
		// staging snapshot is still on disk for forensic recovery.
		m.mu.Lock()
		sb.Status = models.StatusError
		m.mu.Unlock()
		return fmt.Errorf("pause %s: swap snapshot dir: %w", sandboxID, err)
	}

	m.mu.Lock()
	sb.Status = models.StatusPaused
	m.mu.Unlock()

	slog.Info("sandbox paused", "id", sandboxID, "snapshot_dir", finalDir)
	return nil
}

// swapDir atomically replaces final with stage. Any existing final dir is
// moved aside to a uniquely-named trash dir before the swap so the rename
// can succeed, then the trash is removed.
//
// Failure modes:
//   - move-old-to-trash fails: previous final dir is intact. stage remains.
//   - stage-to-final fails: we attempt to restore old from trash. If that
//     fails, the sandbox is wedged but stage still holds valid data.
//   - trash removal fails: previous generation is orphaned, will be GC'd
//     on next agent startup.
func swapDir(stage, final string) error {
	trash := final + ".trash-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	hadOld := true
	if _, err := os.Stat(final); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("stat existing final dir: %w", err)
		}
		hadOld = false
	}
	if hadOld {
		if err := os.Rename(final, trash); err != nil {
			return fmt.Errorf("move old final to trash: %w", err)
		}
	}
	if err := os.Rename(stage, final); err != nil {
		// Try to put the old one back.
		if hadOld {
			if rerr := os.Rename(trash, final); rerr != nil {
				slog.Warn("could not restore previous snapshot dir after failed swap",
					"trash", trash, "final", final, "error", rerr)
			}
		}
		return fmt.Errorf("move stage to final: %w", err)
	}
	if hadOld {
		if err := os.RemoveAll(trash); err != nil {
			slog.Warn("could not remove trashed snapshot dir",
				"path", trash, "error", err)
		}
	}
	return nil
}

// quiesceAndPauseCH drains envd connections, asks envd to quiesce its own
// state, then issues ch.pause. On return the VM is frozen and ready for
// ch.snapshot. Caller must either ch.resume or ch.destroy afterwards.
//
// Snapshot-size optimisation relies on virtio-balloon's free_page_reporting:
// envd drops the VFS page cache + fstrim + a settle window inside
// /snapshot/prepare, which gives the guest balloon driver time to report all
// the now-free pages to the host. CH punches those reports out of the backing
// memfile and v52+'s SEEK_DATA/SEEK_HOLE snapshot writer skips them. No
// explicit balloon inflate is required — inflation would constrain the guest
// post-resume (forced re-allocation of large free regions), and free_page_
// reporting drains everything we'd have inflated anyway.
func (m *Manager) quiesceAndPauseCH(ctx context.Context, sb *sandboxState) error {
	sb.connTracker.Drain(drainTimeout)
	sb.connTracker.ForceClose()

	if err := sb.client.PrepareSnapshot(ctx); err != nil {
		slog.Warn("envd prepare-snapshot failed (continuing)", "id", sb.ID, "error", err)
	}
	sb.client.CloseIdleConnections()

	if err := m.vm.Pause(ctx, sb.ID); err != nil {
		return fmt.Errorf("ch.pause: %w", err)
	}
	return nil
}

// promoteSnapshotDir moves every regular file from srcDir into dstDir using
// rename(2). Renames are per-file so an existing rootfs.ext4 inside dstDir
// that is currently held open by a loop device keeps its inode (the directory
// entry is replaced, but the open fd still references the old inode). srcDir
// is removed on success.
func promoteSnapshotDir(srcDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("mkdir dst: %w", err)
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fmt.Errorf("read staging: %w", err)
	}
	for _, e := range entries {
		from := filepath.Join(srcDir, e.Name())
		to := filepath.Join(dstDir, e.Name())
		if err := os.Rename(from, to); err != nil {
			return fmt.Errorf("rename %s: %w", e.Name(), err)
		}
	}
	return os.RemoveAll(srcDir)
}

// releaseRuntime tears down VM, network, dm-snapshot, and loop refcount for
// a paused sandbox. The CoW file is preserved when keep == keepCow so Resume
// can re-attach it.
type cowDisposition int

const (
	keepCow cowDisposition = iota
	dropCow
)

func (m *Manager) releaseRuntime(sb *sandboxState, cow cowDisposition) {
	// Cancel any background memory loader (UFFD page faulter) before
	// destroying the VM. Without this, the loader keeps trying to fault
	// pages into a vanished guest and races with sb.client being cleared
	// below. Mirror the cleanup() pattern.
	if sb.memLoadCancel != nil {
		sb.memLoadCancel()
		if sb.memLoadDone != nil {
			<-sb.memLoadDone
		}
	}
	m.stopSampler(sb)

	if err := m.vm.Destroy(context.Background(), sb.ID); err != nil {
		slog.Warn("vm destroy on pause", "id", sb.ID, "error", err)
	}
	if err := network.RemoveNetwork(sb.slot); err != nil {
		slog.Warn("network remove on pause", "id", sb.ID, "error", err)
	}
	// Retain the slot when keeping the CoW (pause): Resume must re-acquire
	// the same SlotIndex so the sandbox's host-IP stays stable. Releasing
	// here lets a subsequent Create steal slot 1 while we're paused, and
	// Resume's slots.Reserve() then fails with "slot already in use".
	if cow == dropCow {
		m.slots.Release(sb.SlotIndex)
	}

	if sb.dmDevice != nil {
		if err := devicemapper.RemoveSnapshot(context.Background(), sb.dmDevice); err != nil {
			slog.Warn("dm-snapshot remove on pause", "id", sb.ID, "error", err)
		}
		if cow == dropCow {
			os.Remove(sb.dmDevice.CowPath)
		}
	}
	if sb.baseImagePath != "" {
		m.loops.Release(sb.baseImagePath)
	}

	// Clear runtime references; they're rebuilt on resume.
	sb.slot = nil
	sb.client = nil
	sb.dmDevice = nil
}

// Resume re-launches a paused sandbox from its on-disk snapshot. The same
// SlotIndex is reserved so the sandbox keeps its host-IP. The dm-snapshot
// is re-attached to the existing CoW file, then CH is launched with
// --restore. Memory faults in lazily via userfaultfd.
//
// The snapshot directory is NOT deleted after a successful resume: CH keeps
// an open fd to memory-ranges for lazy page faulting throughout the VM's
// lifetime. The next Pause writes to a fresh staging dir and swaps; only
// then is the previous generation discarded.
//
// The remaining args (defaultUser, env, etc.) are forwarded to envd's /init
// so the resumed sandbox sees the same execution environment as before.
func (m *Manager) Resume(ctx context.Context, sandboxID string, timeoutSec int, defaultUser, _ string, envVars map[string]string) (*models.Sandbox, error) {
	sb, err := m.get(sandboxID)
	if err != nil {
		return nil, err
	}

	sb.lifecycleMu.Lock()
	defer sb.lifecycleMu.Unlock()

	if sb.Status == models.StatusRunning {
		return &sb.Sandbox, nil
	}
	if sb.Status != models.StatusPaused {
		return nil, fmt.Errorf("sandbox %s is not paused (status: %s)", sandboxID, sb.Status)
	}

	snapDir := layout.PauseSnapshotDir(m.cfg.WrennDir, sandboxID)
	meta, err := readSnapshotMeta(snapDir)
	if err != nil {
		return nil, fmt.Errorf("load snapshot meta: %w", err)
	}

	resumed, err := m.resumeFromMeta(ctx, sb, meta, snapDir)
	if err != nil {
		// resumeFromMeta rolled back its own runtime resources. Leave the
		// sandbox in Paused state so the caller can retry — the on-disk
		// snapshot and slot reservation are intact. Evicting from m.boxes
		// would orphan a recoverable sandbox: DB still says paused but the
		// agent would return NotFound on retry.
		m.mu.Lock()
		sb.Status = models.StatusPaused
		m.mu.Unlock()
		return nil, err
	}

	// Single /init with optional defaults — also bumps lifecycle on envd
	// (restarting port forwarder + stepping clock via chronyc).
	//
	// Must complete BEFORE startMemoryLoader: the /init lifecycle bump
	// resets the mem_preload_* atomics in envd. If the loader's POST
	// /memory/preload landed first, the reset would race the running
	// blocking thread and the next GET would observe state="idle", the
	// host goroutine would close memLoadDone, and the next Pause would
	// proceed straight to ch.snapshot before pages were faulted in —
	// producing a memory-ranges file full of holes that read back as zero
	// on the next restore. Sequence: init (reset) → loader (start fresh).
	initCtx, initCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
	if err := resumed.client.PostInitWithDefaults(initCtx, defaultUser, envVars, sandboxID, id.UUIDString(pgtype.UUID{Bytes: sb.TemplateID, Valid: true})); err != nil {
		slog.Warn("post-resume PostInit failed", "id", sandboxID, "error", err)
	}
	initCancel()

	m.startMemoryLoader(resumed)

	if timeoutSec > 0 {
		m.mu.Lock()
		sb.TimeoutSec = timeoutSec
		m.mu.Unlock()
	}

	return &sb.Sandbox, nil
}

// resumeFromMeta wires up the runtime resources (loop, dm-snapshot, network,
// CH process) for a paused sandbox and waits until envd is ready.
//
// On any failure the partial setup is rolled back so the sandbox stays in
// a clean Paused state.
func (m *Manager) resumeFromMeta(ctx context.Context, sb *sandboxState, meta *snapshotMeta, snapDir string) (*sandboxState, error) {
	// 1. Re-acquire the shared loop device for the base template.
	originLoop, err := m.loops.Acquire(meta.BaseTemplate)
	if err != nil {
		return nil, fmt.Errorf("acquire loop: %w", err)
	}
	originSize, err := devicemapper.OriginSizeBytes(originLoop)
	if err != nil {
		m.loops.Release(meta.BaseTemplate)
		return nil, fmt.Errorf("origin size: %w", err)
	}

	// 2. Re-attach the dm-snapshot using the persistent CoW file.
	dmName := "wrenn-" + sb.ID
	dmDev, err := devicemapper.RestoreSnapshot(ctx, dmName, originLoop, meta.CowPath, originSize)
	if err != nil {
		m.loops.Release(meta.BaseTemplate)
		return nil, fmt.Errorf("restore dm-snapshot: %w", err)
	}

	// 3. Slot is already held continuously from Create through Pause —
	// the allocator never released it on Pause, so the SlotIndex from meta
	// is still reserved for this sandbox. Just rebuild the Slot struct.
	slot := network.NewSlot(meta.SlotIndex)

	if err := network.CreateNetwork(slot); err != nil {
		if rmErr := devicemapper.RemoveSnapshot(context.Background(), dmDev); rmErr != nil {
			slog.Warn("dm remove during resume rollback", "id", sb.ID, "error", rmErr)
		}
		m.loops.Release(meta.BaseTemplate)
		return nil, fmt.Errorf("create network: %w", err)
	}

	rollback := func() {
		warnErr("network remove during resume rollback", sb.ID, network.RemoveNetwork(slot))
		// Slot stays reserved across pause/resume — released only on Destroy.
		warnErr("dm remove during resume rollback", sb.ID, devicemapper.RemoveSnapshot(context.Background(), dmDev))
		m.loops.Release(meta.BaseTemplate)
	}

	// 4. Launch CH in restore mode. Lazy memory loading keeps Resume fast.
	vmCfg := vm.VMConfig{
		SandboxID:         sb.ID,
		TemplateID:        id.UUIDString(pgtype.UUID{Bytes: sb.TemplateID, Valid: true}),
		KernelPath:        m.cfg.KernelPath,
		RootfsPath:        dmDev.DevicePath,
		VCPUs:             meta.VCPUs,
		MemoryMB:          meta.MemoryMB,
		NetworkNamespace:  slot.NamespaceID,
		TapDevice:         slot.TapName,
		TapMAC:            slot.TapMAC,
		GuestIP:           slot.GuestIP,
		GatewayIP:         slot.TapIP,
		NetMask:           slot.GuestNetMask,
		VMMBin:            m.cfg.VMMBin,
		LogDir:            filepath.Join(m.cfg.WrennDir, "logs"),
		RestoreFromDir:    snapDir,
		RestoreLazyMemory: true,
	}
	if _, err := m.vm.CreateFromSnapshot(ctx, vmCfg); err != nil {
		rollback()
		return nil, fmt.Errorf("create from snapshot: %w", err)
	}

	// 5. Resume the VM (CreateFromSnapshot leaves it Paused).
	if err := m.vm.Resume(ctx, sb.ID); err != nil {
		_ = m.vm.Destroy(context.Background(), sb.ID)
		rollback()
		return nil, fmt.Errorf("vm resume: %w", err)
	}

	// 6. Wait for envd inside the guest.
	client := envdclient.New(slot.HostIP.String())
	waitCtx, waitCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
	defer waitCancel()
	if err := client.WaitUntilReady(waitCtx); err != nil {
		_ = m.vm.Destroy(context.Background(), sb.ID)
		rollback()
		return nil, fmt.Errorf("wait envd: %w", err)
	}

	// 6a. Deflate the balloon. Free-page reporting drains pages opportunistically
	// while the sandbox runs; the resumed guest needs its full memory budget back.
	// Best-effort: a failure here leaves the guest memory-starved but doesn't
	// break correctness.
	if err := m.vm.UpdateBalloon(ctx, sb.ID, 0); err != nil {
		slog.Warn("balloon deflate after resume failed", "id", sb.ID, "error", err)
	}

	// /init is invoked once by the outer Resume so a single lifecycle bump
	// reaches envd. (Calling it here too would double-restart port forwarder.)

	// 7. Re-hydrate in-memory state.
	m.mu.Lock()
	sb.slot = slot
	sb.client = client
	sb.dmDevice = dmDev
	sb.connTracker.Reset()
	sb.HostIP = slot.HostIP
	sb.RootfsPath = dmDev.DevicePath
	sb.LastActiveAt = time.Now()
	sb.Status = models.StatusRunning
	m.mu.Unlock()

	m.startSampler(sb)
	m.startCrashWatcher(sb)

	// Background memory loader is started by the outer Resume AFTER /init
	// completes — see comment there for the race rationale.

	slog.Info("sandbox resumed", "id", sb.ID, "host_ip", slot.HostIP.String())
	return sb, nil
}

// startMemoryLoader spawns the background goroutine that asks envd to read
// every guest physical page so subsequent snapshots are self-contained. The
// goroutine is cancellable via sb.memLoadCancel and closes sb.memLoadDone on
// exit. Must be called with sb in StatusRunning and sb.client populated.
func (m *Manager) startMemoryLoader(sb *sandboxState) {
	loadCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	m.mu.Lock()
	sb.memLoadCancel = cancel
	sb.memLoadDone = done
	m.mu.Unlock()

	go func() {
		defer close(done)
		client := sb.client
		if client == nil {
			return
		}
		started := time.Now()

		// Kick the loader off in envd. The POST returns as soon as the
		// background thread is queued — actual materialisation continues
		// inside envd independent of this connection.
		startCtx, startCancel := context.WithTimeout(loadCtx, 30*time.Second)
		if _, err := client.StartMemoryPreload(startCtx); err != nil {
			startCancel()
			if loadCtx.Err() != nil {
				slog.Debug("memory preload start cancelled", "id", sb.ID)
				return
			}
			slog.Warn("memory preload start failed", "id", sb.ID, "error", err)
			return
		}
		startCancel()

		// Poll envd for completion. Polling interval is coarse (1s) since the
		// loader runs for many seconds; the polls just check an atomic.
		status, err := client.WaitMemoryPreload(loadCtx)
		if err != nil {
			if loadCtx.Err() != nil {
				slog.Debug("memory preload wait cancelled", "id", sb.ID)
				return
			}
			slog.Warn("memory preload wait failed", "id", sb.ID, "error", err)
			return
		}
		if status.State != "done" {
			slog.Warn("memory preload finished abnormally",
				"id", sb.ID,
				"state", status.State,
				"error", status.Error,
				"pages", status.Pages,
				"bytes", status.Bytes,
				"source", status.Source,
			)
			return
		}
		slog.Info("memory preload complete",
			"id", sb.ID,
			"elapsed", time.Since(started),
			"pages", status.Pages,
			"bytes", status.Bytes,
			"source", status.Source,
		)
	}()
}

// waitForMemoryLoader blocks until the background memory loader finishes, or
// until ctx is cancelled. Returns nil if the loader is already done or not
// running. A pause must wait on this before ch.snapshot so the resulting
// memory-ranges is self-contained.
func (m *Manager) waitForMemoryLoader(ctx context.Context, sb *sandboxState) error {
	m.mu.RLock()
	done := sb.memLoadDone
	m.mu.RUnlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for memory loader: %w", ctx.Err())
	}
}

// CreateSnapshot writes a self-contained template snapshot to
// WRENN_DIR/images/teams/{teamID}/{templateID}/. The sandbox is briefly
// paused, the dm-snapshot is flattened into rootfs.ext4, CH writes the
// memory snapshot, then the sandbox is resumed.
//
// Returns the total size (in bytes) of the artefacts written.
func (m *Manager) CreateSnapshot(ctx context.Context, sandboxID string, teamID, templateID pgtype.UUID) (int64, error) {
	sb, err := m.get(sandboxID)
	if err != nil {
		return 0, err
	}

	sb.lifecycleMu.Lock()
	defer sb.lifecycleMu.Unlock()

	if sb.Status != models.StatusRunning {
		return 0, fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	// Same rationale as Pause: wait for the background memory loader so the
	// resulting memory-ranges is self-contained when this sandbox itself was
	// previously restored from an ondemand snapshot.
	if err := m.waitForMemoryLoader(ctx, sb); err != nil {
		return 0, fmt.Errorf("create snapshot %s: %w", sandboxID, err)
	}

	dstDir := layout.TemplateDir(m.cfg.WrennDir, teamID, templateID)
	stageDir := filepath.Join(layout.SandboxesDir(m.cfg.WrennDir),
		fmt.Sprintf(".stage-%s-%d", sandboxID, time.Now().UnixNano()))
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir stage dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	// Quiesce + ch.pause + ch.snapshot into a staging dir. The final dst
	// may contain the sandbox's own base rootfs.ext4 held open via the loop
	// device; writing through a staging dir + per-file rename avoids
	// unlinking that inode while the loop still references it.
	if err := m.quiesceAndPauseCH(ctx, sb); err != nil {
		_ = m.vm.Resume(context.Background(), sandboxID)
		sb.connTracker.Reset()
		return 0, err
	}
	if err := m.vm.Snapshot(ctx, sandboxID, stageDir); err != nil {
		_ = m.vm.Resume(context.Background(), sandboxID)
		sb.connTracker.Reset()
		return 0, fmt.Errorf("vm.snapshot: %w", err)
	}

	// Flatten dm-snapshot → rootfs.ext4. Reads through the dm device which is
	// stable while CH is paused.
	rootfsOut := filepath.Join(stageDir, "rootfs.ext4")
	if err := devicemapper.FlattenSnapshot(sb.dmDevice.DevicePath, rootfsOut); err != nil {
		// Resume so the sandbox doesn't get stuck. Caller sees the error.
		if rerr := m.vm.Resume(context.Background(), sandboxID); rerr != nil {
			slog.Warn("vm resume after flatten failure", "id", sandboxID, "error", rerr)
		}
		sb.connTracker.Reset()
		return 0, fmt.Errorf("flatten rootfs: %w", err)
	}

	meta := &snapshotMeta{
		SandboxID:    sb.ID,
		TeamID:       id.UUIDString(teamID),
		TemplateID:   id.UUIDString(templateID),
		VCPUs:        sb.VCPUs,
		MemoryMB:     sb.MemoryMB,
		TimeoutSec:   sb.TimeoutSec,
		SlotIndex:    sb.SlotIndex,
		BaseTemplate: sb.baseImagePath,
		CreatedAt:    time.Now(),
	}
	if err := writeSnapshotMeta(stageDir, meta); err != nil {
		slog.Warn("template meta write failed", "id", sandboxID, "error", err)
	}

	// Resume the live sandbox; the staged snapshot is fully written.
	if err := m.vm.Resume(ctx, sandboxID); err != nil {
		return 0, fmt.Errorf("vm resume after live snapshot: %w", err)
	}
	sb.connTracker.Reset()

	// Promote staging → final destination via per-file rename.
	if err := promoteSnapshotDir(stageDir, dstDir); err != nil {
		return 0, fmt.Errorf("promote snapshot: %w", err)
	}

	// Tell envd to refresh its clock and lifecycle. Brief pause means clock
	// drift is usually <1s but PostInit is cheap.
	if err := sb.client.PostInit(ctx); err != nil {
		slog.Warn("envd PostInit after live snapshot", "id", sandboxID, "error", err)
	}

	size, err := snapshot.DirSize(dstDir, "")
	if err != nil {
		slog.Warn("snapshot size calc failed", "id", sandboxID, "error", err)
	}
	slog.Info("live snapshot created",
		"id", sandboxID,
		"team_id", teamID,
		"template_id", templateID,
		"dir", dstDir,
		"bytes", size,
	)
	return size, nil
}

// DeleteSnapshot removes a template snapshot directory. The caller must
// ensure no running sandbox depends on this template before calling.
func (m *Manager) DeleteSnapshot(teamID, templateID pgtype.UUID) error {
	dir := layout.TemplateDir(m.cfg.WrennDir, teamID, templateID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove snapshot dir: %w", err)
	}
	slog.Info("template snapshot deleted", "team_id", teamID, "template_id", templateID)
	return nil
}

// FlattenRootfs writes the current dm-snapshot state to a new template
// rootfs without taking a memory snapshot. Used to publish a sandbox's
// disk-only state as a base image. The sandbox is briefly paused for I/O
// consistency.
func (m *Manager) FlattenRootfs(ctx context.Context, sandboxID string, teamID, templateID pgtype.UUID) (int64, error) {
	sb, err := m.get(sandboxID)
	if err != nil {
		return 0, err
	}

	sb.lifecycleMu.Lock()
	defer sb.lifecycleMu.Unlock()

	if sb.Status != models.StatusRunning {
		return 0, fmt.Errorf("sandbox %s is not running (status: %s)", sandboxID, sb.Status)
	}

	dstDir := layout.TemplateDir(m.cfg.WrennDir, teamID, templateID)
	stageDir := filepath.Join(layout.SandboxesDir(m.cfg.WrennDir),
		fmt.Sprintf(".stage-%s-%d", sandboxID, time.Now().UnixNano()))
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return 0, fmt.Errorf("mkdir stage dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	if err := m.vm.Pause(ctx, sandboxID); err != nil {
		return 0, fmt.Errorf("vm pause for flatten: %w", err)
	}
	flattenErr := devicemapper.FlattenSnapshot(sb.dmDevice.DevicePath, filepath.Join(stageDir, "rootfs.ext4"))
	if rerr := m.vm.Resume(context.Background(), sandboxID); rerr != nil {
		slog.Warn("vm resume after flatten", "id", sandboxID, "error", rerr)
	}
	if flattenErr != nil {
		return 0, fmt.Errorf("flatten: %w", flattenErr)
	}
	if err := promoteSnapshotDir(stageDir, dstDir); err != nil {
		return 0, fmt.Errorf("promote rootfs: %w", err)
	}

	size, err := snapshot.DirSize(dstDir, "")
	if err != nil {
		slog.Warn("flatten size calc failed", "id", sandboxID, "error", err)
	}
	return size, nil
}

// pauseAllConcurrency caps how many sandboxes PauseAll snapshots in
// parallel. Each Pause writes guest RAM to disk and contends on host I/O
// bandwidth, so unbounded parallelism would thrash. 8 keeps a busy host
// from sequential 30s tails without saturating disk on smaller hosts.
const pauseAllConcurrency = 8

// PauseAll pauses every running sandbox. Used by the host agent on graceful
// shutdown so VMs can be resumed by the next agent instance.
//
// Runs Pauses concurrently with a bounded worker pool: per-sandbox Pause
// blocks on the post-resume memory loader (up to 30s) plus ch.snapshot of
// guest RAM (seconds-to-tens-of-seconds), so a serial loop would multiply
// the shutdown budget by the running count. lifecycleMu is per-sandbox so
// there is no cross-sandbox locking; m.mu is taken briefly for status flips.
//
// On each successful Pause, emits a sandbox.auto_paused event synchronously
// so the CP can mark the DB row paused before the agent process exits. Sync
// (not async) because Shutdown fires the process down right after — async
// sends would race with exit. HostMonitor reconciles any event we fail to
// deliver here, but emitting promptly avoids leaving sandboxes stuck as
// 'running' in the DB until the next monitor tick or unreachable threshold.
func (m *Manager) PauseAll(ctx context.Context) {
	m.mu.RLock()
	ids := make([]string, 0, len(m.boxes))
	for id, sb := range m.boxes {
		if sb.Status == models.StatusRunning {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()

	if len(ids) == 0 {
		return
	}

	sem := make(chan struct{}, pauseAllConcurrency)
	var wg sync.WaitGroup
	for _, sbID := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(sbID string) {
			defer wg.Done()
			defer func() { <-sem }()

			if err := m.Pause(ctx, sbID); err != nil {
				slog.Warn("PauseAll: pause failed", "id", sbID, "error", err)
				return
			}
			if m.eventSender == nil {
				return
			}
			if err := m.eventSender.Send(ctx, LifecycleEvent{
				Event:     "sandbox.auto_paused",
				SandboxID: sbID,
			}); err != nil {
				slog.Warn("PauseAll: notify CP failed (reconciler will catch it)", "id", sbID, "error", err)
			}
		}(sbID)
	}
	wg.Wait()
}

// CleanupOrphanPauseDirs removes leftover *.staging-* and *.trash-* dirs
// under sandboxes/ from any Pause that crashed before completing the swap.
// Safe to call at agent startup before any sandbox is created or restored.
//
// Per-sandbox cleanup happens implicitly during Destroy (which removes the
// whole PauseSnapshotDir) — this function only handles agent-crash orphans.
func CleanupOrphanPauseDirs(wrennDir string) {
	sandboxesDir := layout.SandboxesDir(wrennDir)
	entries, err := os.ReadDir(sandboxesDir)
	if err != nil {
		// Directory does not exist yet — nothing to clean.
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.Contains(name, ".staging-") && !strings.Contains(name, ".trash-") {
			continue
		}
		path := filepath.Join(sandboxesDir, name)
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("orphan pause artifact remove failed", "path", path, "error", err)
			continue
		}
		slog.Info("removed orphan pause artifact", "path", path)
	}
}
