package api

import (
	"context"
	"log/slog"
	"time"

	"git.omukk.dev/wrenn/wrenn/pkg/db"
)

// MetricsSampler records per-team sandbox resource usage to
// sandbox_metrics_snapshots every interval. It also prunes rows older than
// 60 days on each tick to keep the table bounded.
type MetricsSampler struct {
	db       *db.Queries
	interval time.Duration
}

// NewMetricsSampler creates a MetricsSampler.
func NewMetricsSampler(queries *db.Queries, interval time.Duration) *MetricsSampler {
	return &MetricsSampler{db: queries, interval: interval}
}

// Start runs the sampler loop until the context is cancelled.
func (s *MetricsSampler) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		// Sample immediately on startup.
		s.run(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.run(ctx)
			}
		}
	}()
}

func (s *MetricsSampler) run(ctx context.Context) {
	s.prune(ctx)
	if err := s.sample(ctx); err != nil {
		slog.Warn("metrics sampler: sample failed", "error", err)
	}
}

func (s *MetricsSampler) sample(ctx context.Context) error {
	rows, err := s.db.SampleSandboxMetrics(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := s.db.InsertMetricsSnapshot(ctx, db.InsertMetricsSnapshotParams(row)); err != nil {
			slog.Warn("metrics sampler: insert snapshot failed", "team_id", row.TeamID, "error", err)
		}
	}
	return nil
}

func (s *MetricsSampler) prune(ctx context.Context) {
	if err := s.db.PruneOldMetrics(ctx); err != nil {
		slog.Warn("metrics sampler: prune failed", "error", err)
	}
}
