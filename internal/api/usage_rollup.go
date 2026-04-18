package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/db"
)

// DailyUsageRollup pre-computes daily CPU-minute and RAM-MB-minute totals
// from sandbox_metrics_snapshots. It runs on startup and then every interval.
type DailyUsageRollup struct {
	db       *db.Queries
	interval time.Duration
}

// NewDailyUsageRollup creates a DailyUsageRollup.
func NewDailyUsageRollup(queries *db.Queries, interval time.Duration) *DailyUsageRollup {
	return &DailyUsageRollup{db: queries, interval: interval}
}

// Start runs the rollup loop until the context is cancelled.
func (r *DailyUsageRollup) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(r.interval)
		defer ticker.Stop()

		// Run immediately on startup.
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

func (r *DailyUsageRollup) run(ctx context.Context) {
	teams, err := r.db.GetTeamsWithSnapshots(ctx)
	if err != nil {
		slog.Warn("usage rollup: failed to get teams", "error", err)
		return
	}

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)

	for _, teamID := range teams {
		// Only roll up yesterday (fully completed day). Today's usage is
		// computed live at query time by UsageService.
		if err := r.rollupDay(ctx, teamID, yesterday); err != nil {
			slog.Warn("usage rollup: failed", "team_id", teamID, "day", yesterday.Format("2006-01-02"), "error", err)
		}
	}
}

func (r *DailyUsageRollup) rollupDay(ctx context.Context, teamID pgtype.UUID, day time.Time) error {
	dayStart := day
	dayEnd := day.Add(24 * time.Hour)

	row, err := r.db.ComputeDailyUsageForDay(ctx, db.ComputeDailyUsageForDayParams{
		TeamID:      teamID,
		SampledAt:   pgtype.Timestamptz{Time: dayStart, Valid: true},
		SampledAt_2: pgtype.Timestamptz{Time: dayEnd, Valid: true},
	})
	if err != nil {
		return err
	}

	return r.db.UpsertDailyUsage(ctx, db.UpsertDailyUsageParams{
		TeamID:       teamID,
		Day:          pgtype.Date{Time: day, Valid: true},
		CpuMinutes:   row.CpuMinutes,
		RamMbMinutes: row.RamMbMinutes,
	})
}
