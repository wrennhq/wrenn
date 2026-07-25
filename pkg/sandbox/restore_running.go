package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"time"

	"git.omukk.dev/wrenn/wrenn/pkg/devicemapper"
	"git.omukk.dev/wrenn/wrenn/pkg/envdclient"
	"git.omukk.dev/wrenn/wrenn/pkg/layout"
	"git.omukk.dev/wrenn/wrenn/pkg/models"
	"git.omukk.dev/wrenn/wrenn/pkg/network"
	"git.omukk.dev/wrenn/wrenn/pkg/vm"
)

// reattachProbeTimeout bounds the CH API probe issued while re-attaching to
// a live VM. Local unix-socket call — kept tight so a dead socket fails fast.
const reattachProbeTimeout = 3 * time.Second

// RestoreRunningSandboxes scans WRENN_DIR/sandboxes/ for sandboxes whose
// creating process has exited but whose VMs are still running, and re-attaches
// them to this manager. This is what lets a short-lived CLI process Exec into
// or Destroy a sandbox created by an earlier invocation.
//
// A sandbox qualifies when its dir contains a sandbox.json running-state file
// (written on create/resume, removed by every teardown path) AND its CH
// process is verifiably alive: the recorded PID exists, its cmdline still
// references this sandbox (PID-recycling guard), and the CH API socket
// answers. Anything else is a stale record from a crash — its leftover
// resources are swept, but a pause snapshot in the same dir is preserved so
// RestorePausedSandboxes can still recover it.
//
// Must be called once at startup, BEFORE RestorePausedSandboxes: a resumed
// sandbox's dir contains both the running-state file and the previous
// generation's pause snapshot, and the running state must win while the VM
// is alive.
//
// Never returns an error — a single unrecoverable sandbox must not block
// startup. Mirrors RestorePausedSandboxes.
func (m *Manager) RestoreRunningSandboxes() {
	sandboxesDir := layout.SandboxesDir(m.cfg.WrennDir)
	entries, err := os.ReadDir(sandboxesDir)
	if err != nil {
		// Directory does not exist yet — fresh install, nothing to restore.
		return
	}

	restored, swept := 0, 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.Contains(name, ".staging-") || strings.Contains(name, ".trash-") {
			continue
		}

		st, err := readRunningState(m.cfg.WrennDir, name)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("restore running: unreadable state file, removing",
					"id", name, "error", err)
				deleteRunningState(m.cfg.WrennDir, name)
			}
			continue
		}

		m.mu.RLock()
		_, known := m.boxes[name]
		m.mu.RUnlock()
		if known {
			continue
		}

		if !chProcessAlive(st) {
			m.sweepDeadRunning(st)
			swept++
			continue
		}

		if err := m.reattachRunning(st); err != nil {
			slog.Warn("restore running: re-attach failed", "id", name, "error", err)
			continue
		}
		restored++
	}

	if restored > 0 || swept > 0 {
		slog.Info("running sandbox restore complete", "restored", restored, "swept", swept)
	}
}

// chProcessAlive reports whether the recorded CH process is still the CH
// process for this sandbox: PID alive, cmdline still references the sandbox
// (guards against PID recycling), API socket present.
func chProcessAlive(st *runningState) bool {
	if st.CHPID <= 0 {
		return false
	}
	if err := syscall.Kill(st.CHPID, 0); err != nil {
		return false
	}
	// The PID is the unshare wrapper whose bash script embeds the sandbox's
	// socket path and dm device path — both contain the sandbox ID.
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", st.CHPID))
	if err != nil || !strings.Contains(string(cmdline), st.ID) {
		return false
	}
	if _, err := os.Stat(st.CHSocket); err != nil {
		return false
	}
	return true
}

// reattachRunning rebuilds the in-memory sandboxState for a verified-live
// sandbox and registers it in m.boxes.
func (m *Manager) reattachRunning(st *runningState) error {
	teamBytes, err := parsePlainUUID(st.TeamID)
	if err != nil {
		return fmt.Errorf("bad team_id: %w", err)
	}
	templateBytes, err := parsePlainUUID(st.TemplateID)
	if err != nil {
		return fmt.Errorf("bad template_id: %w", err)
	}
	if st.SlotIndex == 0 {
		return fmt.Errorf("running state has no slot_index")
	}

	// Adopt the slot (its claim file already exists — written by the creating
	// process) so a concurrent Create cannot be handed this sandbox's IP.
	if err := m.slots.Reserve(st.SlotIndex); err != nil {
		return fmt.Errorf("reserve slot %d: %w", st.SlotIndex, err)
	}
	slot := network.NewSlot(st.SlotIndex)

	// NO rollback past this point. chProcessAlive just verified a live CH
	// process that depends on this slot's netns/IP, the dm device, and the
	// loop attachments. Releasing any of it on a failed adoption would pull
	// resources out from under a running VM: freeing the slot lets a new
	// Create collide with the live netns, and dropping the loop refcount can
	// detach the origin loop the dm table references (LoopRegistry adopts the
	// previous process's loop, so refcount 1 here IS that live attachment).
	// On failure, leave everything held and unmanaged — the next agent
	// restart retries the adoption, or sweeps properly once CH is dead.

	// Rediscover the live dm-snapshot; no device-mapper state is modified.
	dmDev, err := devicemapper.ReattachSnapshot(st.DMName, st.CowPath)
	if err != nil {
		return fmt.Errorf("reattach dm-snapshot (slot %d left reserved for live CH): %w",
			st.SlotIndex, err)
	}

	// Re-acquire the base image in this process's loop registry so the
	// refcounting stays balanced with the Release in cleanup()/releaseRuntime.
	// (The dm device keeps referencing the creating process's origin loop by
	// device number; this Acquire only matters for bookkeeping and for other
	// sandboxes sharing the base in this process.)
	if _, err := m.loops.Acquire(st.BaseImagePath); err != nil {
		return fmt.Errorf("acquire base image loop (slot %d left reserved for live CH): %w",
			st.SlotIndex, err)
	}

	// Register the live CH process; probes the API socket before committing.
	probeCtx, cancel := context.WithTimeout(context.Background(), reattachProbeTimeout)
	defer cancel()
	_, err = m.vm.Reattach(probeCtx, vm.VMConfig{
		SandboxID:  st.ID,
		SocketPath: st.CHSocket,
		SandboxDir: st.SandboxDir,
		VCPUs:      st.VCPUs,
		MemoryMB:   st.MemoryMB,
	}, st.CHPID)
	if err != nil {
		return fmt.Errorf("reattach VM (slot %d and loop refcount left held for live CH): %w",
			st.SlotIndex, err)
	}

	sb := &sandboxState{
		Sandbox: models.Sandbox{
			ID:             st.ID,
			Status:         models.StatusRunning,
			TemplateTeamID: teamBytes,
			TemplateID:     templateBytes,
			VCPUs:          st.VCPUs,
			MemoryMB:       st.MemoryMB,
			TimeoutSec:     st.TimeoutSec,
			SlotIndex:      st.SlotIndex,
			HostIP:         slot.HostIP,
			RootfsPath:     dmDev.DevicePath,
			CreatedAt:      st.CreatedAt,
			LastActiveAt:   time.Now(),
			Metadata:       st.Metadata,
		},
		slot:               slot,
		connTracker:        &ConnTracker{},
		dmDevice:           dmDev,
		baseImagePath:      st.BaseImagePath,
		sandboxDirOverride: st.SandboxDirOverride,
		lazyRestore:        st.LazyRestore,
		volumes:            st.Volumes,
	}
	sb.client.Store(envdclient.New(slot.HostIP.String()))

	m.mu.Lock()
	m.boxes[st.ID] = sb
	m.mu.Unlock()

	// A lazily-restored VM may still be materialising guest memory when the
	// creating process died. Re-arm the loader: envd's POST is CAS-idempotent
	// (a completed preload answers "done" immediately), and having memLoadDone
	// set again restores the TTL-reaper/activity-sampler guards. Pause remains
	// safe either way — ensureMemoryMaterialized verifies against envd itself.
	if sb.lazyRestore {
		m.startMemoryLoader(sb)
	}

	m.startSampler(sb)
	m.startCrashWatcher(sb)

	slog.Info("re-attached running sandbox",
		"id", st.ID, "slot", st.SlotIndex, "pid", st.CHPID, "host_ip", slot.HostIP.String())
	return nil
}

// sweepDeadRunning cleans up after a sandbox whose running-state file exists
// but whose VM is gone (creating process crashed, host rebooted, VM killed).
// Best-effort: every resource may or may not still exist.
//
// A pause snapshot in the same dir is preserved — the sandbox may have been
// resumed and then died, in which case the previous pause generation is still
// the best available recovery point and RestorePausedSandboxes (called after
// this) will register it. Without a snapshot the dir is removed entirely.
func (m *Manager) sweepDeadRunning(st *runningState) {
	slog.Info("restore running: sweeping dead sandbox", "id", st.ID)

	if dmDev, err := devicemapper.ReattachSnapshot(st.DMName, st.CowPath); err == nil {
		if err := devicemapper.RemoveSnapshot(context.Background(), dmDev); err != nil {
			slog.Warn("sweep: dm-snapshot remove failed", "id", st.ID, "error", err)
		}
	}
	// ReattachSnapshot usually fails here: Setup's CleanupStaleDevices already
	// removed the dead sandbox's dm device (it was not held open by any live
	// CH), so RemoveSnapshot above never runs and the CoW loop from the
	// previous agent stays attached. Detach it by backing file before the
	// os.RemoveAll below deletes that file and orphans the loop for good.
	devicemapper.DetachLoopsByFile(st.CowPath)
	if err := network.RemoveNetwork(network.NewSlot(st.SlotIndex)); err != nil {
		slog.Warn("sweep: network remove failed", "id", st.ID, "error", err)
	}
	_ = os.Remove(st.CHSocket)
	deleteRunningState(m.cfg.WrennDir, st.ID)

	snapDir := layout.PauseSnapshotDir(m.cfg.WrennDir, st.ID)
	if _, err := readSnapshotMeta(snapDir); err == nil {
		// Recoverable pause snapshot present: keep the dir, the CoW file, and
		// the slot claim so RestorePausedSandboxes can adopt them.
		return
	}
	if err := os.RemoveAll(layout.SandboxDir(m.cfg.WrennDir, st.ID)); err != nil {
		slog.Warn("sweep: sandbox dir remove failed", "id", st.ID, "error", err)
	}
	if st.SlotIndex > 0 {
		m.slots.Release(st.SlotIndex)
	}
}
