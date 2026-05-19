package sandbox

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"git.omukk.dev/wrenn/wrenn/internal/layout"
	"git.omukk.dev/wrenn/wrenn/internal/models"
)

// RestorePausedSandboxes scans WRENN_DIR/sandboxes/ for paused-sandbox
// snapshots left behind by a previous agent instance and re-registers them
// in m.boxes as StatusPaused. Without this, ListSandboxes would not report
// these sandboxes, and the CP's HostMonitor would mark them stopped via
// the missing-confirmed-dead reconcile path — orphaning the on-disk
// snapshot dir and surfacing a leaked "stopped" sandbox to users.
//
// Restored sandboxes hold ONLY the slot reservation; VM / network / dm /
// loop refcount stay unowned until Resume rebuilds them. baseImagePath is
// deliberately NOT set on the in-memory entry so cleanup() does not call
// loops.Release on a loop that was never Acquire'd — the registry tolerates
// a Release of an unknown key, but a coincident-same-base running sandbox
// would have its refcount decremented incorrectly.
//
// Must be called once at agent startup, AFTER CleanupOrphanPauseDirs (so
// .staging-* / .trash-* dirs are gone) and BEFORE the HTTP server starts
// serving — otherwise an early Create RPC can race the slot reservation.
//
// Corrupt snapshot dirs (unparseable meta, missing slot index) are renamed
// to .trash-{ts}/ so a future CleanupOrphanPauseDirs sweeps them. Soft
// errors are logged; this function never returns an error — startup should
// not fail because a single sandbox is unrecoverable.
func (m *Manager) RestorePausedSandboxes() {
	sandboxesDir := layout.SandboxesDir(m.cfg.WrennDir)
	entries, err := os.ReadDir(sandboxesDir)
	if err != nil {
		// Directory does not exist yet — fresh install, nothing to restore.
		return
	}

	type candidate struct {
		sandboxID string
		snapDir   string
		meta      *snapshotMeta
		teamID    [16]byte
		templID   [16]byte
	}

	// Pass 1: parse every snapshot meta. Trash anything unreadable or
	// missing the slot index — those are crash artefacts, not recoverable
	// sandboxes.
	candidates := make([]candidate, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Skip CleanupOrphanPauseDirs's territory. If it ran before us
		// these are already gone; if not, leave them alone.
		if strings.Contains(name, ".staging-") || strings.Contains(name, ".trash-") {
			continue
		}

		snapDir := layout.PauseSnapshotDir(m.cfg.WrennDir, name)
		meta, err := readSnapshotMeta(snapDir)
		if err != nil {
			slog.Warn("restore: unreadable snapshot meta, trashing dir",
				"id", name, "error", err)
			trashCorruptDir(snapDir)
			continue
		}
		if meta.SlotIndex == 0 {
			slog.Warn("restore: snapshot has no slot_index, trashing dir", "id", name)
			trashCorruptDir(snapDir)
			continue
		}
		teamBytes, err := parsePlainUUID(meta.TeamID)
		if err != nil {
			slog.Warn("restore: bad team_id in snapshot meta", "id", name, "error", err)
			trashCorruptDir(snapDir)
			continue
		}
		templateBytes, err := parsePlainUUID(meta.TemplateID)
		if err != nil {
			slog.Warn("restore: bad template_id in snapshot meta", "id", name, "error", err)
			trashCorruptDir(snapDir)
			continue
		}
		candidates = append(candidates, candidate{
			sandboxID: name,
			snapDir:   snapDir,
			meta:      meta,
			teamID:    teamBytes,
			templID:   templateBytes,
		})
	}

	// Pass 2: bucket by slot index, pick the newest CreatedAt per slot.
	// Multiple candidates per slot happen when older paused-sandbox dirs
	// were left on disk by the pre-fix leak (DB row marked stopped but the
	// snapshot was never cleaned). The newest is the most likely live one;
	// older losers are trashed so CleanupOrphanPauseDirs sweeps them on
	// the next startup.
	bySlot := make(map[int][]candidate, len(candidates))
	for _, c := range candidates {
		bySlot[c.meta.SlotIndex] = append(bySlot[c.meta.SlotIndex], c)
	}

	restored := 0
	pruned := 0
	for slot, cands := range bySlot {
		sort.Slice(cands, func(i, j int) bool {
			return cands[i].meta.CreatedAt.After(cands[j].meta.CreatedAt)
		})

		// Trash every loser. The host_monitor's zombie-cleanup path catches
		// the winner if its DB row says 'stopped' — but losers never enter
		// m.boxes and would otherwise sit on disk indefinitely.
		for _, stale := range cands[1:] {
			slog.Info("restore: pruning older snapshot for same slot",
				"id", stale.sandboxID, "slot", slot, "created", stale.meta.CreatedAt,
				"winner", cands[0].sandboxID, "winner_created", cands[0].meta.CreatedAt)
			trashCorruptDir(stale.snapDir)
			pruned++
		}

		winner := cands[0]
		if err := m.slots.Reserve(winner.meta.SlotIndex); err != nil {
			// Reserve only fails if another candidate (different slot value
			// in meta but same numeric index) already grabbed it, or if the
			// allocator is corrupt. Either way the snapshot is unusable
			// without a slot, so trash it.
			slog.Warn("restore: slot reservation failed, trashing dir",
				"id", winner.sandboxID, "slot", winner.meta.SlotIndex, "error", err)
			trashCorruptDir(winner.snapDir)
			pruned++
			continue
		}

		sb := &sandboxState{
			Sandbox: models.Sandbox{
				ID:             winner.sandboxID,
				Status:         models.StatusPaused,
				TemplateTeamID: winner.teamID,
				TemplateID:     winner.templID,
				VCPUs:          winner.meta.VCPUs,
				MemoryMB:       winner.meta.MemoryMB,
				TimeoutSec:     winner.meta.TimeoutSec,
				SlotIndex:      winner.meta.SlotIndex,
				CreatedAt:      winner.meta.CreatedAt,
				// LastActiveAt cosmetic only — TTL reaper ignores non-Running.
				LastActiveAt: winner.meta.CreatedAt,
			},
			// connTracker must be non-nil: resumeFromMeta calls Reset() on it
			// unconditionally during rehydration. A nil pointer would panic.
			connTracker: &ConnTracker{},
			// baseImagePath intentionally left empty — see function doc.
			// sandboxDirOverride intentionally left empty — resumeFromMeta
			// reads meta.SandboxDir from disk on the resume path.
		}

		m.mu.Lock()
		m.boxes[winner.sandboxID] = sb
		m.mu.Unlock()
		restored++

		slog.Info("restored paused sandbox", "id", winner.sandboxID,
			"slot", winner.meta.SlotIndex, "vcpus", winner.meta.VCPUs, "memory_mb", winner.meta.MemoryMB)
	}

	if restored > 0 || pruned > 0 {
		slog.Info("paused sandbox restore complete", "restored", restored, "pruned", pruned)
	}
}

// parsePlainUUID turns a standard hyphenated UUID string (as produced by
// id.UUIDString) back into the 16-byte representation used by sandboxState.
func parsePlainUUID(s string) ([16]byte, error) {
	if s == "" {
		return [16]byte{}, fmt.Errorf("empty uuid string")
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return [16]byte{}, err
	}
	return [16]byte(u), nil
}

// trashCorruptDir renames a corrupt snapshot directory aside so a future
// CleanupOrphanPauseDirs sweeps it. Best-effort: if rename fails we log
// and move on — leaving the directory in place is safe (restore will skip
// it again next startup) but unwanted.
func trashCorruptDir(dir string) {
	parent := filepath.Dir(dir)
	base := filepath.Base(dir)
	trash := filepath.Join(parent, fmt.Sprintf("%s.trash-%d", base, time.Now().UnixNano()))
	if err := os.Rename(dir, trash); err != nil {
		slog.Warn("restore: failed to trash corrupt snapshot dir",
			"src", dir, "dst", trash, "error", err)
	}
}
