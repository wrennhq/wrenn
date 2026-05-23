package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

// errInferredTransientTimeout marks a state change that the reconciler
// inferred after a transient (starting/resuming) sandbox failed to settle
// within the grace period. Used as the err value on system audit calls so
// the published event carries Outcome=error with a human-readable message.
var errInferredTransientTimeout = errors.New("transient state did not settle within grace period")

// unreachableThreshold is how long a host can go without a heartbeat before
// it is considered unreachable (3 missed 30-second heartbeats).
const unreachableThreshold = 90 * time.Second

// transientGracePeriod is how long a sandbox is allowed to stay in a transient
// status (starting, resuming, pausing, stopping) before the monitor infers a
// final state. This prevents the monitor from racing against in-flight RPCs
// that may not have registered the sandbox on the host agent yet.
const transientGracePeriod = 2 * time.Minute

// snapshotGracePeriod is the grace for a sandbox stuck in "snapshotting" while
// the VM is still alive on the host. Snapshots dump guest RAM and flatten the
// rootfs, which can run for minutes on large sandboxes, and the agent reports
// the VM as alive throughout — so we must not race the in-flight operation.
// It exceeds the background goroutine's 10-minute deadline, so reaching it
// means the control plane crashed mid-snapshot and the sandbox needs recovery.
const snapshotGracePeriod = 15 * time.Minute

// HostMonitor runs on a fixed interval and performs two duties:
//
//  1. Passive check: marks hosts whose last_heartbeat_at is stale as
//     "unreachable" and marks their active sandboxes as "missing".
//
//  2. Active reconciliation: for each online host, calls ListSandboxes and
//     reconciles DB state against live host state — restoring "missing"
//     sandboxes that are actually alive, and stopping orphaned ones.
type HostMonitor struct {
	db       *db.Queries
	pool     *lifecycle.HostClientPool
	audit    *audit.AuditLogger
	interval time.Duration
}

// NewHostMonitor creates a HostMonitor.
func NewHostMonitor(queries *db.Queries, pool *lifecycle.HostClientPool, al *audit.AuditLogger, interval time.Duration) *HostMonitor {
	return &HostMonitor{
		db:       queries,
		pool:     pool,
		audit:    al,
		interval: interval,
	}
}

// Start runs the monitor loop until the context is cancelled.
func (m *HostMonitor) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()

		// Run immediately on startup so the CP doesn't wait one full interval
		// before reconciling host and sandbox state.
		m.run(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.run(ctx)
			}
		}
	}()
}

func (m *HostMonitor) run(ctx context.Context) {
	hosts, err := m.db.ListActiveHosts(ctx)
	if err != nil {
		slog.Warn("host monitor: failed to list hosts", "error", err)
		return
	}

	for _, host := range hosts {
		m.checkHost(ctx, host)
	}
}

// ReconcileHost triggers immediate active reconciliation for a single host.
// Called when a host transitions from unreachable → online so sandboxes marked
// "missing" are resolved without waiting for the next monitor tick.
func (m *HostMonitor) ReconcileHost(ctx context.Context, hostID pgtype.UUID) {
	host, err := m.db.GetHost(ctx, hostID)
	if err != nil {
		slog.Warn("host monitor: reconcile-on-connect: failed to get host", "error", err)
		return
	}
	if host.Status != "online" {
		return
	}
	m.checkHost(ctx, host)
}

func (m *HostMonitor) checkHost(ctx context.Context, host db.Host) {
	// --- Passive phase: check heartbeat staleness ---

	stale := !host.LastHeartbeatAt.Valid ||
		time.Since(host.LastHeartbeatAt.Time) > unreachableThreshold

	if stale && host.Status != "unreachable" {
		slog.Info("host monitor: marking host unreachable", "host_id", id.FormatHostID(host.ID),
			"last_heartbeat", host.LastHeartbeatAt.Time)
		if err := m.db.MarkHostUnreachable(ctx, host.ID); err != nil {
			slog.Warn("host monitor: failed to mark host unreachable", "host_id", id.FormatHostID(host.ID), "error", err)
		}
		if err := m.db.MarkSandboxesMissingByHost(ctx, host.ID); err != nil {
			slog.Warn("host monitor: failed to mark sandboxes missing", "host_id", id.FormatHostID(host.ID), "error", err)
		}
		m.audit.LogHostMarkedDown(ctx, host.TeamID, host.ID)
		return
	}

	// --- Active reconciliation: only for online hosts ---

	if host.Status != "online" {
		return
	}

	agent, err := m.pool.GetForHost(host)
	if err != nil {
		// Host has no address yet (e.g., just registered) — skip.
		return
	}

	resp, err := agent.ListSandboxes(ctx, connect.NewRequest(&pb.ListSandboxesRequest{}))
	if err != nil {
		// RPC failure is a transient condition; the passive phase will catch it
		// if heartbeats stop arriving.
		slog.Debug("host monitor: ListSandboxes failed (transient)", "host_id", id.FormatHostID(host.ID), "error", err)
		return
	}

	// Build map of sandbox ID -> reported status. Transient statuses
	// (pausing/resuming/starting/stopping) are coerced to a presence-only
	// entry: ListSandboxes can observe the in-memory status mid-transition
	// (Pause flips the status under m.mu while List holds m.mu.RLock), and
	// writing those transient labels into the DB would force the transient
	// reconciliation phase to wait the full grace period before resolving.
	// Recording the presence keeps "missing → restore" and "running →
	// orphan-stop" logic correct without overwriting with stale labels;
	// the next monitor tick reads the settled status.
	aliveStatus := make(map[string]string, len(resp.Msg.Sandboxes))
	for _, sb := range resp.Msg.Sandboxes {
		status := sb.Status
		switch status {
		case "pausing", "resuming", "starting", "stopping":
			status = ""
		}
		aliveStatus[sb.SandboxId] = status
	}

	// --- Restore sandboxes that are "missing" in DB but alive on host ---
	// Handles transient heartbeat gaps where the host was actually fine. The
	// reported status must be honored: a sandbox the agent paused while CP
	// was disconnected must not be silently promoted back to running.

	missingSandboxes, err := m.db.ListSandboxesByHostAndStatus(ctx, db.ListSandboxesByHostAndStatusParams{
		HostID:  host.ID,
		Column2: []string{"missing"},
	})
	if err != nil {
		slog.Warn("host monitor: failed to list missing sandboxes", "host_id", id.FormatHostID(host.ID), "error", err)
	} else {
		restoreByStatus := make(map[string][]db.Sandbox)
		var toStop []db.Sandbox
		for _, sb := range missingSandboxes {
			sbIDStr := id.FormatSandboxID(sb.ID)
			status, ok := aliveStatus[sbIDStr]
			if !ok {
				toStop = append(toStop, sb)
				continue
			}
			if status == "" {
				continue
			}
			restoreByStatus[status] = append(restoreByStatus[status], sb)
		}
		for status, sbs := range restoreByStatus {
			ids := make([]pgtype.UUID, len(sbs))
			for i, sb := range sbs {
				ids[i] = sb.ID
			}
			slog.Info("host monitor: restoring missing sandboxes", "host_id", id.FormatHostID(host.ID), "status", status, "count", len(ids))
			if err := m.db.BulkRestoreMissingToStatus(ctx, db.BulkRestoreMissingToStatusParams{
				Column1: ids,
				Status:  status,
			}); err != nil {
				slog.Warn("host monitor: failed to restore missing sandboxes", "host_id", id.FormatHostID(host.ID), "status", status, "error", err)
				continue
			}
			// Only restore→paused emits a notification (per design: running restore is silent).
			if status == "paused" {
				for _, sb := range sbs {
					m.audit.LogSandboxAutoPause(ctx, sb.TeamID, sb.ID, "restored_after_host_recovery", nil)
				}
			}
		}
		if len(toStop) > 0 {
			ids := make([]pgtype.UUID, len(toStop))
			for i, sb := range toStop {
				ids[i] = sb.ID
			}
			slog.Info("host monitor: stopping confirmed-dead missing sandboxes", "host_id", id.FormatHostID(host.ID), "count", len(ids))
			if err := m.db.BulkUpdateStatusByIDs(ctx, db.BulkUpdateStatusByIDsParams{
				Column1: ids,
				Status:  "stopped",
			}); err != nil {
				slog.Warn("host monitor: failed to stop missing sandboxes", "host_id", id.FormatHostID(host.ID), "error", err)
			} else {
				for _, sb := range toStop {
					m.audit.LogSandboxDestroySystem(ctx, sb.TeamID, sb.ID, "orphaned", nil)
				}
			}
		}
	}

	// --- Reconcile running sandboxes in DB against live host state ---
	// Three cases per DB-running row:
	//   absent on host          -> stopped
	//   present and running     -> no change
	//   present but paused/etc. -> sync DB to reported status (catches the
	//                              shutdown-pause notify failure case)

	runningSandboxes, err := m.db.ListSandboxesByHostAndStatus(ctx, db.ListSandboxesByHostAndStatusParams{
		HostID:  host.ID,
		Column2: []string{"running"},
	})
	if err != nil {
		slog.Warn("host monitor: failed to list running sandboxes", "host_id", id.FormatHostID(host.ID), "error", err)
		return
	}

	var toStop []db.Sandbox
	syncByStatus := make(map[string][]db.Sandbox)
	for _, sb := range runningSandboxes {
		sbIDStr := id.FormatSandboxID(sb.ID)
		status, ok := aliveStatus[sbIDStr]
		if !ok {
			toStop = append(toStop, sb)
			continue
		}
		if status == "running" || status == "" {
			continue
		}
		syncByStatus[status] = append(syncByStatus[status], sb)
	}

	if len(toStop) > 0 {
		ids := make([]pgtype.UUID, len(toStop))
		for i, sb := range toStop {
			ids[i] = sb.ID
		}
		slog.Info("host monitor: marking orphaned sandboxes stopped", "host_id", id.FormatHostID(host.ID), "count", len(ids))
		if err := m.db.BulkUpdateStatusByIDs(ctx, db.BulkUpdateStatusByIDsParams{
			Column1: ids,
			Status:  "stopped",
		}); err != nil {
			slog.Warn("host monitor: failed to mark stopped", "host_id", id.FormatHostID(host.ID), "error", err)
		} else {
			for _, sb := range toStop {
				m.audit.LogSandboxDestroySystem(ctx, sb.TeamID, sb.ID, "orphaned", nil)
			}
		}
	}
	for status, sbs := range syncByStatus {
		ids := make([]pgtype.UUID, len(sbs))
		for i, sb := range sbs {
			ids[i] = sb.ID
		}
		slog.Info("host monitor: syncing running→reported status", "host_id", id.FormatHostID(host.ID), "status", status, "count", len(ids))
		if err := m.db.BulkUpdateStatusByIDs(ctx, db.BulkUpdateStatusByIDsParams{
			Column1: ids,
			Status:  status,
		}); err != nil {
			slog.Warn("host monitor: failed to sync running sandboxes", "host_id", id.FormatHostID(host.ID), "status", status, "error", err)
			continue
		}
		if status == "paused" {
			for _, sb := range sbs {
				m.audit.LogSandboxAutoPause(ctx, sb.TeamID, sb.ID, "host_state_sync", nil)
			}
		}
	}

	// --- Reconcile DB-stopped + agent-paused zombies ---
	// A sandbox the agent reports as 'paused' but DB has as 'stopped' is an
	// orphan from a previous bug where a successful pause's auto_paused
	// callback was lost (e.g. CP unreachable during agent shutdown). With the
	// agent-side fix (RestorePausedSandboxes), the snapshot survives across
	// agent restarts and surfaces here. Authoritative direction: DB wins
	// (user already saw 'stopped' and may have stopped tracking it).
	// Issue Destroy so the on-disk snapshot dir is removed and the agent's
	// slot reservation released.
	//
	// Gate: only run the DB query if the agent reports at least one paused
	// sandbox. Otherwise we'd fetch every historically-stopped sandbox on
	// this host every monitor tick — unbounded growth over a host's lifetime.
	hasPaused := false
	for _, status := range aliveStatus {
		if status == "paused" {
			hasPaused = true
			break
		}
	}
	if hasPaused {
		stoppedSandboxes, err := m.db.ListSandboxesByHostAndStatus(ctx, db.ListSandboxesByHostAndStatusParams{
			HostID:  host.ID,
			Column2: []string{"stopped"},
		})
		if err != nil {
			slog.Warn("host monitor: failed to list stopped sandboxes", "host_id", id.FormatHostID(host.ID), "error", err)
		} else {
			for _, sb := range stoppedSandboxes {
				sbIDStr := id.FormatSandboxID(sb.ID)
				status, ok := aliveStatus[sbIDStr]
				if !ok || status != "paused" {
					continue
				}
				slog.Info("host monitor: destroying DB-stopped agent-paused zombie",
					"host_id", id.FormatHostID(host.ID), "sandbox_id", sbIDStr)
				if _, err := agent.DestroySandbox(ctx, connect.NewRequest(&pb.DestroySandboxRequest{
					SandboxId: sbIDStr,
				})); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
					slog.Warn("host monitor: zombie destroy failed",
						"sandbox_id", sbIDStr, "error", err)
					continue
				}
				m.audit.LogSandboxDestroySystem(ctx, sb.TeamID, sb.ID, "paused_zombie_cleanup", nil)
			}
		}
	}

	// --- Reconcile transient statuses (starting, resuming, pausing, stopping) ---
	// These represent in-flight operations. If the sandbox is no longer alive on
	// the host, infer the final state based on the transient status.

	transientSandboxes, err := m.db.ListSandboxesByHostAndStatus(ctx, db.ListSandboxesByHostAndStatusParams{
		HostID:  host.ID,
		Column2: []string{"starting", "resuming", "pausing", "stopping", "snapshotting"},
	})
	if err != nil {
		slog.Warn("host monitor: failed to list transient sandboxes", "host_id", id.FormatHostID(host.ID), "error", err)
		return
	}

	for _, sb := range transientSandboxes {
		sbIDStr := id.FormatSandboxID(sb.ID)
		if agentStatus, ok := aliveStatus[sbIDStr]; ok {
			// Sandbox is alive on host — the background goroutine should
			// finalize the transition. For starting/resuming, if the sandbox
			// is alive it means creation/resume succeeded.
			if sb.Status == "starting" || sb.Status == "resuming" {
				if _, err := m.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
					ID: sb.ID, Status: sb.Status, Status_2: "running",
				}); err == nil {
					slog.Info("host monitor: promoted transient sandbox to running", "sandbox_id", sbIDStr, "from", sb.Status)
				}
			}
			// A snapshot keeps the source sandbox alive throughout, so an alive
			// sandbox does NOT mean the snapshot finished. Only recover it once
			// it has been stuck past the snapshot grace period (i.e. the CP
			// crashed mid-op). Recover to the sandbox's actual host-side status:
			// a running sandbox is snapshotted live and stays running, but a
			// paused sandbox is snapshotted from disk and must return to paused.
			if sb.Status == "snapshotting" &&
				sb.LastUpdated.Valid && time.Since(sb.LastUpdated.Time) >= snapshotGracePeriod {
				recoverTo := agentStatus
				if recoverTo != "running" && recoverTo != "paused" {
					// Coerced/unknown agent label — default to running.
					recoverTo = "running"
				}
				if _, err := m.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
					ID: sb.ID, Status: "snapshotting", Status_2: recoverTo,
				}); err == nil {
					slog.Info("host monitor: recovered stuck snapshotting sandbox", "sandbox_id", sbIDStr, "to", recoverTo)
					m.audit.LogSnapshotCreateSystem(ctx, sb.TeamID, sb.ID, "snapshot_recovered", nil)
				}
			}
			continue
		}
		// Sandbox is not alive on host. If the transition is recent, give the
		// in-flight RPC time to finish before declaring a final state.
		if sb.LastUpdated.Valid && time.Since(sb.LastUpdated.Time) < transientGracePeriod {
			slog.Debug("host monitor: transient sandbox still within grace period",
				"sandbox_id", sbIDStr, "status", sb.Status,
				"age", time.Since(sb.LastUpdated.Time).Round(time.Second))
			continue
		}

		// Grace period expired — infer final state.
		var finalStatus string
		switch sb.Status {
		case "starting", "resuming":
			finalStatus = "error"
		case "pausing":
			finalStatus = "paused"
		case "stopping":
			finalStatus = "stopped"
		case "snapshotting":
			// VM is gone but DB says snapshotting → the snapshot died with the VM.
			finalStatus = "error"
		}
		fromStatus := sb.Status
		if _, err := m.db.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
			ID: sb.ID, Status: fromStatus, Status_2: finalStatus,
		}); err == nil {
			slog.Info("host monitor: resolved transient sandbox", "sandbox_id", sbIDStr, "from", fromStatus, "to", finalStatus)
			inferredErr := errInferredTransientTimeout
			switch fromStatus {
			case "starting":
				m.audit.LogSandboxCreateSystem(ctx, sb.TeamID, sb.ID, "transient_timeout", inferredErr)
			case "resuming":
				m.audit.LogSandboxResumeSystem(ctx, sb.TeamID, sb.ID, "transient_timeout", inferredErr)
			case "pausing":
				// Pause assumed to have succeeded host-side; emit success with inferred metadata.
				m.audit.LogSandboxAutoPause(ctx, sb.TeamID, sb.ID, "transient_timeout_inferred", nil)
			case "snapshotting":
				// VM gone mid-snapshot; the sandbox is errored.
				m.audit.LogSnapshotCreateSystem(ctx, sb.TeamID, sb.ID, "transient_timeout", inferredErr)
			case "stopping":
				m.audit.LogSandboxDestroySystem(ctx, sb.TeamID, sb.ID, "transient_timeout_inferred", nil)
			}
		}
	}
}
