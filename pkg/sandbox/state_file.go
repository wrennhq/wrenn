package sandbox

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/layout"
	"git.omukk.dev/wrenn/wrenn/pkg/vm"
)

// uuidString16 renders a raw 16-byte UUID as the standard hyphenated string.
func uuidString16(b [16]byte) string {
	return id.UUIDString(pgtype.UUID{Bytes: b, Valid: true})
}

// runningStateFile is the per-sandbox state file that records everything a
// fresh process needs to re-attach to a RUNNING sandbox created by an earlier
// process (see RestoreRunningSandboxes). It lives in the sandbox dir alongside
// rootfs.cow, so every existing teardown path already removes it:
//
//   - Pause's swapDir replaces the dir with a staging dir that lacks it
//   - Destroy's os.RemoveAll(SandboxDir) wipes it with the dir
//   - cleanupAfterCrash's os.RemoveAll(SandboxDir) likewise
//
// It is therefore only present while the sandbox is (or last was) running.
const runningStateFile = "sandbox.json"

// runningStateVersion is bumped on breaking schema changes; readers refuse
// unknown versions rather than mis-restoring.
const runningStateVersion = 1

// runningState is the on-disk schema of runningStateFile.
type runningState struct {
	Version    int    `json:"version"`
	ID         string `json:"id"`
	TeamID     string `json:"team_id"`
	TemplateID string `json:"template_id"`
	VCPUs      int    `json:"vcpus"`
	MemoryMB   int    `json:"memory_mb"`
	TimeoutSec int    `json:"timeout_sec"`
	SlotIndex  int    `json:"slot_index"`
	// BaseImagePath is the base template rootfs backing the dm-snapshot.
	BaseImagePath string `json:"base_image_path"`
	CowPath       string `json:"cow_path"`
	DMName        string `json:"dm_name"`
	// SandboxDir is the tmpfs path baked into CH's config (effectiveSandboxDir) —
	// differs from vm.SandboxTmpDir(ID) for sandboxes launched from snapshot
	// templates.
	SandboxDir string `json:"sandbox_dir"`
	// CHPID is the PID of the unshare wrapper; the exec chain keeps the same
	// PID, so it is also the cloud-hypervisor PID.
	CHPID    int    `json:"ch_pid"`
	CHSocket string `json:"ch_socket"`
	// SandboxDirOverride is non-empty when the sandbox was launched from a
	// snapshot template (see sandboxState.sandboxDirOverride).
	SandboxDirOverride string            `json:"sandbox_dir_override,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	Metadata           map[string]string `json:"metadata,omitempty"`
}

// writeRunningState persists sb's re-attach state next to its CoW file.
// Best-effort by design: the sandbox is already running and functional, so a
// failed write only means a later process cannot re-attach it. Must be called
// after the VM is registered in m.vm (the PID is read from there).
func (m *Manager) writeRunningState(sb *sandboxState) {
	v, ok := m.vm.Get(sb.ID)
	if !ok {
		slog.Warn("running state: VM not found, skipping persist", "id", sb.ID)
		return
	}

	st := runningState{
		Version:            runningStateVersion,
		ID:                 sb.ID,
		TeamID:             uuidString16(sb.TemplateTeamID),
		TemplateID:         uuidString16(sb.TemplateID),
		VCPUs:              sb.VCPUs,
		MemoryMB:           sb.MemoryMB,
		TimeoutSec:         sb.TimeoutSec,
		SlotIndex:          sb.SlotIndex,
		CowPath:            sb.dmDevice.CowPath,
		DMName:             sb.dmDevice.Name,
		BaseImagePath:      sb.baseImagePath,
		SandboxDir:         effectiveSandboxDir(sb),
		CHPID:              v.PID(),
		CHSocket:           vm.SandboxSocketPath(sb.ID),
		SandboxDirOverride: sb.sandboxDirOverride,
		CreatedAt:          sb.CreatedAt,
		Metadata:           sb.Metadata,
	}

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		slog.Warn("running state: marshal failed", "id", sb.ID, "error", err)
		return
	}

	// Atomic write (tmp + rename) so a concurrent reader never sees a torn file.
	path := runningStatePath(m.cfg.WrennDir, sb.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		slog.Warn("running state: write failed", "id", sb.ID, "error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		slog.Warn("running state: rename failed", "id", sb.ID, "error", err)
	}
}

// readRunningState loads and validates the running-state file for a sandbox.
func readRunningState(wrennDir, sandboxID string) (*runningState, error) {
	data, err := os.ReadFile(runningStatePath(wrennDir, sandboxID))
	if err != nil {
		return nil, err
	}
	var st runningState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("unmarshal running state: %w", err)
	}
	if st.Version != runningStateVersion {
		return nil, fmt.Errorf("unsupported running state version %d (want %d)", st.Version, runningStateVersion)
	}
	if st.ID != sandboxID {
		return nil, fmt.Errorf("running state id mismatch: file says %q, dir is %q", st.ID, sandboxID)
	}
	return &st, nil
}

// deleteRunningState removes the running-state file. Most teardown paths do
// not need this (they remove or swap the whole sandbox dir); it exists for
// the dead-sandbox path in RestoreRunningSandboxes, which must invalidate the
// file while preserving a possible pause snapshot in the same dir.
func deleteRunningState(wrennDir, sandboxID string) {
	_ = os.Remove(runningStatePath(wrennDir, sandboxID))
}

func runningStatePath(wrennDir, sandboxID string) string {
	return filepath.Join(layout.SandboxDir(wrennDir, sandboxID), runningStateFile)
}
