package api

import (
	"context"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	"git.omukk.dev/wrenn/sandbox/internal/db"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
)

// Reconciler periodically compares the host agent's sandbox list with the DB
// and marks sandboxes that no longer exist on the host as stopped.
type Reconciler struct {
	db       *db.Queries
	agent    hostagentv1connect.HostAgentServiceClient
	hostID   string
	interval time.Duration
}

// NewReconciler creates a new reconciler.
func NewReconciler(db *db.Queries, agent hostagentv1connect.HostAgentServiceClient, hostID string, interval time.Duration) *Reconciler {
	return &Reconciler{
		db:       db,
		agent:    agent,
		hostID:   hostID,
		interval: interval,
	}
}

// Start runs the reconciliation loop until the context is cancelled.
func (rc *Reconciler) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(rc.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rc.reconcile(ctx)
			}
		}
	}()
}

func (rc *Reconciler) reconcile(ctx context.Context) {
	// Get all sandboxes the host agent knows about.
	resp, err := rc.agent.ListSandboxes(ctx, connect.NewRequest(&pb.ListSandboxesRequest{}))
	if err != nil {
		slog.Warn("reconciler: failed to list sandboxes from host agent", "error", err)
		return
	}

	// Build a set of sandbox IDs that are alive on the host.
	alive := make(map[string]struct{}, len(resp.Msg.Sandboxes))
	for _, sb := range resp.Msg.Sandboxes {
		alive[sb.SandboxId] = struct{}{}
	}

	// Get all DB sandboxes for this host that are running or paused.
	dbSandboxes, err := rc.db.ListSandboxesByHostAndStatus(ctx, db.ListSandboxesByHostAndStatusParams{
		HostID:  rc.hostID,
		Column2: []string{"running", "paused"},
	})
	if err != nil {
		slog.Warn("reconciler: failed to list DB sandboxes", "error", err)
		return
	}

	// Find sandboxes in DB that are no longer on the host.
	var stale []string
	for _, sb := range dbSandboxes {
		if _, ok := alive[sb.ID]; !ok {
			stale = append(stale, sb.ID)
		}
	}

	if len(stale) == 0 {
		return
	}

	slog.Info("reconciler: marking stale sandboxes as stopped", "count", len(stale), "ids", stale)

	if err := rc.db.BulkUpdateStatusByIDs(ctx, db.BulkUpdateStatusByIDsParams{
		Column1: stale,
		Status:  "stopped",
	}); err != nil {
		slog.Warn("reconciler: failed to update stale sandboxes", "error", err)
	}
}
