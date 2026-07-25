package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/db"
)

// VolumeReaper periodically frees volumes stuck in a transient state
// (attaching/deleting) whose owning operation died — e.g. a control-plane crash
// between reserving a volume and the capsule booting, or a delete that crashed
// mid-flight. It is the safety net for the one volume-lifecycle window that no
// synchronous path can clean up (a reserved volume carries no sandbox_id, so the
// terminal-status detach can't reach it).
//
// A volume is released only once it has been stuck longer than staleAfter, which
// MUST exceed the capsule create timeout so an in-flight attach is never freed
// out from under a booting capsule.
type VolumeReaper struct {
	db         *db.Queries
	interval   time.Duration
	staleAfter time.Duration
}

// NewVolumeReaper creates a VolumeReaper that sweeps every interval and releases
// volumes stuck in a transient state for longer than staleAfter.
func NewVolumeReaper(queries *db.Queries, interval, staleAfter time.Duration) *VolumeReaper {
	return &VolumeReaper{db: queries, interval: interval, staleAfter: staleAfter}
}

// Start runs the reaper loop until the context is cancelled.
func (r *VolumeReaper) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		// Run immediately on startup so a crash-orphaned reservation is cleared
		// without waiting a full interval.
		r.run(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.run(ctx)
			}
		}
	}()
}

func (r *VolumeReaper) run(ctx context.Context) {
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-r.staleAfter), Valid: true}
	n, err := r.db.ReleaseStaleVolumeReservations(ctx, cutoff)
	if err != nil {
		slog.Warn("volume reaper: failed to release stale reservations", "error", err)
		return
	}
	if n > 0 {
		slog.Info("volume reaper: released stale volume reservations", "count", n)
	}
}
