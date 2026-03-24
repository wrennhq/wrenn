package api

import (
	"context"
	"log/slog"
	"time"

	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"

	"connectrpc.com/connect"
)

// unreachableThreshold is how long a host can go without a heartbeat before
// it is considered unreachable (3 missed 30-second heartbeats).
const unreachableThreshold = 90 * time.Second

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
	interval time.Duration
}

// NewHostMonitor creates a HostMonitor.
func NewHostMonitor(queries *db.Queries, pool *lifecycle.HostClientPool, interval time.Duration) *HostMonitor {
	return &HostMonitor{
		db:       queries,
		pool:     pool,
		interval: interval,
	}
}

// Start runs the monitor loop until the context is cancelled.
func (m *HostMonitor) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()

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

func (m *HostMonitor) checkHost(ctx context.Context, host db.Host) {
	// --- Passive phase: check heartbeat staleness ---

	stale := !host.LastHeartbeatAt.Valid ||
		time.Since(host.LastHeartbeatAt.Time) > unreachableThreshold

	if stale && host.Status != "unreachable" {
		slog.Info("host monitor: marking host unreachable", "host_id", host.ID,
			"last_heartbeat", host.LastHeartbeatAt.Time)
		if err := m.db.MarkHostUnreachable(ctx, host.ID); err != nil {
			slog.Warn("host monitor: failed to mark host unreachable", "host_id", host.ID, "error", err)
		}
		if err := m.db.MarkSandboxesMissingByHost(ctx, host.ID); err != nil {
			slog.Warn("host monitor: failed to mark sandboxes missing", "host_id", host.ID, "error", err)
		}
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
		slog.Debug("host monitor: ListSandboxes failed (transient)", "host_id", host.ID, "error", err)
		return
	}

	// Build set of sandbox IDs alive on the host.
	alive := make(map[string]struct{}, len(resp.Msg.Sandboxes))
	for _, sb := range resp.Msg.Sandboxes {
		alive[sb.SandboxId] = struct{}{}
	}

	autoPaused := make(map[string]struct{}, len(resp.Msg.AutoPausedSandboxIds))
	for _, id := range resp.Msg.AutoPausedSandboxIds {
		autoPaused[id] = struct{}{}
	}

	// --- Restore sandboxes that are "missing" in DB but alive on host ---
	// This handles the case where CP marked them missing due to a transient
	// heartbeat gap, but the host was actually fine.

	missingSandboxes, err := m.db.ListSandboxesByHostAndStatus(ctx, db.ListSandboxesByHostAndStatusParams{
		HostID:  host.ID,
		Column2: []string{"missing"},
	})
	if err != nil {
		slog.Warn("host monitor: failed to list missing sandboxes", "host_id", host.ID, "error", err)
	} else {
		var toRestore []string
		var toStop []string
		for _, sb := range missingSandboxes {
			if _, ok := alive[sb.ID]; ok {
				toRestore = append(toRestore, sb.ID)
			} else {
				toStop = append(toStop, sb.ID)
			}
		}
		if len(toRestore) > 0 {
			slog.Info("host monitor: restoring missing sandboxes", "host_id", host.ID, "count", len(toRestore))
			if err := m.db.BulkRestoreRunning(ctx, toRestore); err != nil {
				slog.Warn("host monitor: failed to restore missing sandboxes", "host_id", host.ID, "error", err)
			}
		}
		if len(toStop) > 0 {
			slog.Info("host monitor: stopping confirmed-dead missing sandboxes", "host_id", host.ID, "count", len(toStop))
			if err := m.db.BulkUpdateStatusByIDs(ctx, db.BulkUpdateStatusByIDsParams{
				Column1: toStop,
				Status:  "stopped",
			}); err != nil {
				slog.Warn("host monitor: failed to stop missing sandboxes", "host_id", host.ID, "error", err)
			}
		}
	}

	// --- Find running sandboxes in DB that are no longer alive on the host ---

	runningSandboxes, err := m.db.ListSandboxesByHostAndStatus(ctx, db.ListSandboxesByHostAndStatusParams{
		HostID:  host.ID,
		Column2: []string{"running"},
	})
	if err != nil {
		slog.Warn("host monitor: failed to list running sandboxes", "host_id", host.ID, "error", err)
		return
	}

	var toPause, toStop []string
	for _, sb := range runningSandboxes {
		if _, ok := alive[sb.ID]; ok {
			continue
		}
		if _, ok := autoPaused[sb.ID]; ok {
			toPause = append(toPause, sb.ID)
		} else {
			toStop = append(toStop, sb.ID)
		}
	}

	if len(toPause) > 0 {
		slog.Info("host monitor: marking auto-paused sandboxes", "host_id", host.ID, "count", len(toPause))
		if err := m.db.BulkUpdateStatusByIDs(ctx, db.BulkUpdateStatusByIDsParams{
			Column1: toPause,
			Status:  "paused",
		}); err != nil {
			slog.Warn("host monitor: failed to mark paused", "host_id", host.ID, "error", err)
		}
	}
	if len(toStop) > 0 {
		slog.Info("host monitor: marking orphaned sandboxes stopped", "host_id", host.ID, "count", len(toStop))
		if err := m.db.BulkUpdateStatusByIDs(ctx, db.BulkUpdateStatusByIDsParams{
			Column1: toStop,
			Status:  "stopped",
		}); err != nil {
			slog.Warn("host monitor: failed to mark stopped", "host_id", host.ID, "error", err)
		}
	}
}
