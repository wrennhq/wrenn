// Package sandbox: shared CH-restore helpers used by both Resume (paused →
// running) and the snapshot-template launch path (template → fresh sandbox).
//
// The two callers diverge in how they acquire resources (slot, dm-snapshot,
// sandbox identity) but converge on:
//
//	build VMConfig → CreateFromSnapshot → vm.Resume → wait envd → balloon deflate
//
// These steps are extracted here so the sequence — and its quirks (paused
// post-restore state, balloon best-effort, restored disk path baked into
// CH's config.json) — has a single source of truth.
package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"git.omukk.dev/wrenn/wrenn/pkg/envdclient"
	"git.omukk.dev/wrenn/wrenn/pkg/network"
	"git.omukk.dev/wrenn/wrenn/pkg/vm"
)

// restoreInputs is the common set of fields needed to build a restore VMConfig.
type restoreInputs struct {
	sandboxID  string // VM identity for the new CH process (sock path, log file)
	templateID string // forwarded to envd via PostInit (informational)
	snapDir    string // directory containing CH snapshot artefacts
	rootfsPath string // /dev/mapper/wrenn-{newID} — per-sandbox dm-snapshot
	vcpus      int
	memoryMB   int
	slot       *network.Slot
	sandboxDir string // override for VMConfig.SandboxDir; "" = default
}

// buildRestoreVMConfig assembles the VMConfig used to launch a CH process in
// restore mode. sandboxDir, when non-empty, overrides the default
// "/tmp/ch-vm-{SandboxID}" — required when the snapshot's saved config.json
// points at a different sandbox's tmpfs path (i.e. snapshot-template launch).
func (m *Manager) buildRestoreVMConfig(in restoreInputs) vm.VMConfig {
	return vm.VMConfig{
		SandboxID:         in.sandboxID,
		TemplateID:        in.templateID,
		KernelPath:        m.cfg.KernelPath,
		RootfsPath:        in.rootfsPath,
		VCPUs:             in.vcpus,
		MemoryMB:          in.memoryMB,
		NetworkNamespace:  in.slot.NamespaceID,
		TapDevice:         in.slot.TapName,
		TapMAC:            in.slot.TapMAC,
		GuestIP:           in.slot.GuestIP,
		GatewayIP:         in.slot.TapIP,
		NetMask:           in.slot.GuestNetMask,
		VMMBin:            m.cfg.VMMBin,
		LogDir:            filepath.Join(m.cfg.WrennDir, "logs"),
		RestoreFromDir:    in.snapDir,
		RestoreLazyMemory: true,
		SandboxDir:        in.sandboxDir,
	}
}

// launchRestoredVM starts CH in restore mode, resumes the vCPUs, waits for
// envd to be reachable, then best-effort deflates the balloon. On any failure
// the partial VM is destroyed before returning — the caller is responsible
// for tearing down dm/network/slot.
//
// Returns the connected envd client on success.
func (m *Manager) launchRestoredVM(ctx context.Context, vmCfg vm.VMConfig, hostIP string) (*envdclient.Client, error) {
	if _, err := m.vm.CreateFromSnapshot(ctx, vmCfg); err != nil {
		return nil, fmt.Errorf("create from snapshot: %w", err)
	}

	if err := m.vm.Resume(ctx, vmCfg.SandboxID); err != nil {
		_ = m.vm.Destroy(context.Background(), vmCfg.SandboxID)
		return nil, fmt.Errorf("vm resume: %w", err)
	}

	client := envdclient.New(hostIP)
	waitCtx, waitCancel := context.WithTimeout(ctx, envdReadyTimeout(vmCfg.MemoryMB))
	defer waitCancel()
	if err := client.WaitUntilReady(waitCtx); err != nil {
		_ = m.vm.Destroy(context.Background(), vmCfg.SandboxID)
		return nil, fmt.Errorf("%w after resume: %w", ErrEnvdNotReady, err)
	}

	// Best-effort balloon deflate. Free-page reporting drains pages while the
	// sandbox runs; the resumed guest needs its full memory budget back. A
	// failure leaves the guest memory-starved but doesn't break correctness.
	if err := m.vm.UpdateBalloon(ctx, vmCfg.SandboxID, 0); err != nil {
		slog.Warn("balloon deflate after restore failed", "id", vmCfg.SandboxID, "error", err)
	}

	return client, nil
}

// initAndStartMemoryLoader runs envd's /init lifecycle bump and then kicks
// off the background memory loader. Ordering matters: /init resets envd's
// mem_preload_* atomics, so the loader's POST /memory/preload must land
// after — otherwise the next CreateSnapshot/Pause would observe a stale
// "idle" state and snapshot a memfile full of holes.
//
// Must be called with sb already registered in m.boxes with StatusRunning
// and sb.client populated.
func (m *Manager) initAndStartMemoryLoader(ctx context.Context, sb *sandboxState, defaultUser, templateIDStr string, envVars map[string]string) {
	initCtx, initCancel := context.WithTimeout(ctx, m.cfg.EnvdTimeout)
	defer initCancel()
	c := sb.client.Load()
	if c == nil {
		slog.Warn("post-restore PostInit skipped: envd client cleared", "id", sb.ID)
		return
	}
	if err := c.PostInitWithDefaults(initCtx, defaultUser, envVars, sb.ID, templateIDStr, m.cfg.ProxyDomain); err != nil {
		slog.Warn("post-restore PostInit failed", "id", sb.ID, "error", err)
	}

	m.startMemoryLoader(sb)
}
