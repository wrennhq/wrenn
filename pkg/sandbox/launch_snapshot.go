// Package sandbox: launching a fresh sandbox from a snapshot template.
//
// Mirrors the pause/resume restore path but produces a brand-new sandbox each
// call: fresh ID, fresh network slot, fresh CoW on top of the template's
// flattened rootfs. The CH process is launched with --restore + lazy memory
// (UFFD), and the post-restore memory loader is started so any subsequent
// CreateSnapshot taken from this descendant is self-contained (the
// pause-resume-pause chain guarantee, applied to template lineages).
package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/devicemapper"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/layout"
	"git.omukk.dev/wrenn/wrenn/pkg/models"
	"git.omukk.dev/wrenn/wrenn/pkg/network"
)

// createFromSnapshotTemplate launches a new sandbox from a snapshot-template
// directory (state.json + config.json + memory-ranges + rootfs.ext4).
//
// The caller has already verified IsSnapshotTemplate(templateDir). Resources
// acquired here are rolled back on any failure; on success the sandbox is
// registered in m.boxes and runs in StatusRunning.
func (m *Manager) createFromSnapshotTemplate(
	ctx context.Context,
	sandboxID string,
	teamID, templateID pgtype.UUID,
	vcpus, memoryMB, timeoutSec, diskSizeMB int,
	defaultUser string,
	defaultEnv map[string]string,
) (*models.Sandbox, int64, error) {
	templateDir := layout.TemplateDir(m.cfg.WrennDir, teamID, templateID)
	baseRootfs := layout.TemplateRootfs(m.cfg.WrennDir, teamID, templateID)

	meta, err := readSnapshotMeta(templateDir)
	if err != nil {
		return nil, 0, fmt.Errorf("read snapshot meta: %w", err)
	}
	if meta.SandboxDir == "" {
		// CH's saved config.json hardcodes a tmpfs disk path; meta.SandboxDir
		// is that exact path. A snapshot template without it cannot be launched.
		return nil, 0, fmt.Errorf("snapshot template %s missing sandbox_dir in meta", templateDir)
	}

	// Acquire shared read-only loop on the flattened rootfs. Many sandboxes
	// can share this loop concurrently — refcounted in LoopRegistry.
	originLoop, err := m.loops.Acquire(baseRootfs)
	if err != nil {
		return nil, 0, fmt.Errorf("acquire loop: %w", err)
	}
	originSize, err := devicemapper.OriginSizeBytes(originLoop)
	if err != nil {
		m.loops.Release(baseRootfs)
		return nil, 0, fmt.Errorf("origin size: %w", err)
	}

	// Per-sandbox CoW on top of the shared origin.
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

	slotIdx, err := m.slots.Allocate()
	if err != nil {
		res.rollback()
		return nil, 0, fmt.Errorf("allocate network slot: %w", err)
	}
	res.slotIdx = slotIdx
	slot := network.NewSlot(slotIdx)

	if err := network.CreateNetwork(slot); err != nil {
		res.rollback()
		return nil, 0, fmt.Errorf("create network: %w", err)
	}
	res.slot = slot

	// CH's saved config.json hardcodes a tmpfs disk path; meta.SandboxDir is
	// that exact path (carried forward verbatim across template chains, so a
	// snapshot-of-a-snapshot resolves to the root ancestor's path). The
	// launcher mounts a fresh tmpfs there inside its private mount namespace
	// and symlinks rootfs.ext4 → our new dm device.
	vmCfg := m.buildRestoreVMConfig(restoreInputs{
		sandboxID:  sandboxID,
		templateID: id.UUIDString(templateID),
		snapDir:    templateDir,
		rootfsPath: dmDev.DevicePath,
		vcpus:      vcpus,
		memoryMB:   memoryMB,
		slot:       slot,
		sandboxDir: meta.SandboxDir,
	})

	client, err := m.launchRestoredVM(ctx, vmCfg, slot.HostIP.String())
	if err != nil {
		res.rollback()
		return nil, 0, err
	}
	res.vm = m.vm

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
		slot:               slot,
		connTracker:        &ConnTracker{},
		dmDevice:           dmDev,
		baseImagePath:      baseRootfs,
		sandboxDirOverride: meta.SandboxDir,
		lazyRestore:        true,
	}
	sb.client.Store(client)

	m.mu.Lock()
	m.boxes[sandboxID] = sb
	m.mu.Unlock()

	// /init lifecycle bump then start the memory loader. Loader is required
	// so any future CreateSnapshot taken from this descendant captures all
	// guest pages (otherwise SEEK_DATA/SEEK_HOLE would emit holes for the
	// still-lazy UFFD pages — silent corruption across template chains).
	m.initAndStartMemoryLoader(ctx, sb, defaultUser, id.UUIDString(templateID), defaultEnv)

	m.startSampler(sb)
	m.startCrashWatcher(sb)
	m.writeRunningState(sb)

	slog.Info("sandbox launched from snapshot template",
		"id", sandboxID,
		"team_id", teamID,
		"template_id", templateID,
		"sandbox_dir", meta.SandboxDir,
		"host_ip", slot.HostIP.String(),
		"dm_device", dmDev.DevicePath,
	)

	return &sb.Sandbox, cowSize, nil
}

// templateExists returns true if a snapshot template already lives at
// TemplateDir(team, templateID). Used by CreateSnapshot to refuse silent
// overwrites — every snapshot must land in a fresh templateID.
func (m *Manager) templateExists(teamID, templateID pgtype.UUID) bool {
	dir := layout.TemplateDir(m.cfg.WrennDir, teamID, templateID)
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	return layout.IsSnapshotTemplate(dir)
}
