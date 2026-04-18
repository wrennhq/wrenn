package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.omukk.dev/wrenn/wrenn/pkg/db"
)

// TimeRange identifies a chart time window.
type TimeRange string

const (
	Range5m  TimeRange = "5m"
	Range1h  TimeRange = "1h"
	Range6h  TimeRange = "6h"
	Range24h TimeRange = "24h"
	Range30d TimeRange = "30d"
)

type rangeConfig struct {
	bucketSec       int    // bucket width in seconds for time-series aggregation
	intervalLiteral string // PostgreSQL interval literal for the lookback window
}

var rangeConfigs = map[TimeRange]rangeConfig{
	Range5m:  {bucketSec: 3, intervalLiteral: "5 minutes"},
	Range1h:  {bucketSec: 30, intervalLiteral: "1 hour"},
	Range6h:  {bucketSec: 180, intervalLiteral: "6 hours"},
	Range24h: {bucketSec: 720, intervalLiteral: "24 hours"},
	Range30d: {bucketSec: 21600, intervalLiteral: "30 days"},
}

// ValidRange returns true if r is a known TimeRange value.
func ValidRange(r TimeRange) bool {
	_, ok := rangeConfigs[r]
	return ok
}

// StatPoint is one bucketed data point in the time-series.
type StatPoint struct {
	Bucket           time.Time
	RunningCount     int32
	VCPUsReserved    int32
	MemoryMBReserved int32
}

// CurrentStats holds the live values for a team, read directly from sandboxes.
type CurrentStats struct {
	RunningCount     int32
	VCPUsReserved    int32
	MemoryMBReserved int32
}

// PeakStats holds the 30-day maximum values for a team.
type PeakStats struct {
	RunningCount int32
	VCPUs        int32
	MemoryMB     int32
}

// StatsService computes sandbox metrics for the dashboard.
type StatsService struct {
	DB   *db.Queries
	Pool *pgxpool.Pool
}

// GetStats returns current stats, 30-day peaks, and a time-series for the
// given team and time range. If no snapshots exist yet, zeros are returned.
func (s *StatsService) GetStats(ctx context.Context, teamID pgtype.UUID, r TimeRange) (CurrentStats, PeakStats, []StatPoint, error) {
	cfg, ok := rangeConfigs[r]
	if !ok {
		return CurrentStats{}, PeakStats{}, nil, fmt.Errorf("unknown range: %s", r)
	}

	// Current live values — read directly from sandboxes so we always reflect
	// the true state even when no capsules are running.
	cur, err := s.DB.GetLiveMetrics(ctx, teamID)
	if err != nil {
		return CurrentStats{}, PeakStats{}, nil, fmt.Errorf("get live metrics: %w", err)
	}
	current := CurrentStats{
		RunningCount:     cur.RunningCount,
		VCPUsReserved:    cur.VcpusReserved,
		MemoryMBReserved: cur.MemoryMbReserved,
	}

	// 30-day peaks.
	var peaks PeakStats
	pk, err := s.DB.GetPeakMetrics(ctx, teamID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CurrentStats{}, PeakStats{}, nil, fmt.Errorf("get peak metrics: %w", err)
	}
	if err == nil {
		peaks = PeakStats{
			RunningCount: pk.PeakRunningCount,
			VCPUs:        pk.PeakVcpus,
			MemoryMB:     pk.PeakMemoryMb,
		}
	}

	// Time-series — dynamic bucket width, executed via pgx directly.
	series, err := s.queryTimeSeries(ctx, teamID, cfg)
	if err != nil {
		return CurrentStats{}, PeakStats{}, nil, fmt.Errorf("get time series: %w", err)
	}

	return current, peaks, series, nil
}

// timeSeriesSQL uses an epoch-floor trick to bucket rows by an arbitrary
// integer number of seconds without requiring TimescaleDB.
//
// MAX is used instead of AVG so that short-lived running states are not
// averaged down to zero within a bucket. For capacity metrics the peak
// value in each bucket is what matters — AVG with ::INTEGER rounding
// caused running_count, vcpus, and memory to become inconsistent with
// each other (e.g. running=0 but vcpus=1).
//
// $1 = bucket width in seconds (integer)
// $2 = team_id
// $3 = lookback interval literal (e.g. '1 hour')
const timeSeriesSQL = `
SELECT
    to_timestamp(floor(extract(epoch FROM sampled_at) / $1) * $1) AS bucket,
    MAX(running_count)                 AS running_count,
    MAX(vcpus_reserved)                AS vcpus_reserved,
    MAX(memory_mb_reserved)            AS memory_mb_reserved
FROM sandbox_metrics_snapshots
WHERE team_id = $2
  AND sampled_at >= NOW() - $3::INTERVAL
GROUP BY bucket
ORDER BY bucket ASC
`

func (s *StatsService) queryTimeSeries(ctx context.Context, teamID pgtype.UUID, cfg rangeConfig) ([]StatPoint, error) {
	rows, err := s.Pool.Query(ctx, timeSeriesSQL, cfg.bucketSec, teamID, cfg.intervalLiteral)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []StatPoint
	for rows.Next() {
		var p StatPoint
		var bucket time.Time
		if err := rows.Scan(&bucket, &p.RunningCount, &p.VCPUsReserved, &p.MemoryMBReserved); err != nil {
			return nil, err
		}
		p.Bucket = bucket
		points = append(points, p)
	}
	return points, rows.Err()
}

// UsagePoint is one daily usage data point.
type UsagePoint struct {
	Day          time.Time
	CPUMinutes   float64
	RAMMBMinutes float64
}

// UsageService queries pre-computed daily usage rollups. For the current
// day it computes usage live from sandbox_metrics_snapshots so the value
// is always up-to-date rather than stale until the next hourly rollup.
type UsageService struct {
	DB *db.Queries
}

// GetUsage returns daily CPU-minute and RAM-MB-minute totals for a team
// within the given date range (inclusive). Past days come from the
// pre-computed daily_usage table; today is computed live from snapshots.
func (s *UsageService) GetUsage(ctx context.Context, teamID pgtype.UUID, from, to time.Time) ([]UsagePoint, error) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	// Clamp the pre-computed query to exclude today (it hasn't been rolled up).
	precomputedTo := to
	if !to.Before(today) {
		precomputedTo = today.AddDate(0, 0, -1)
	}

	var points []UsagePoint

	// Fetch pre-computed days (from..min(to, yesterday)).
	if !from.After(precomputedTo) {
		rows, err := s.DB.GetDailyUsage(ctx, db.GetDailyUsageParams{
			TeamID: teamID,
			Day:    pgtype.Date{Time: from, Valid: true},
			Day_2:  pgtype.Date{Time: precomputedTo, Valid: true},
		})
		if err != nil {
			return nil, fmt.Errorf("get daily usage: %w", err)
		}

		points = make([]UsagePoint, 0, len(rows)+1)
		for _, r := range rows {
			cpu, err := r.CpuMinutes.Float64Value()
			if err != nil {
				return nil, fmt.Errorf("convert cpu_minutes: %w", err)
			}
			ram, err := r.RamMbMinutes.Float64Value()
			if err != nil {
				return nil, fmt.Errorf("convert ram_mb_minutes: %w", err)
			}
			points = append(points, UsagePoint{
				Day:          r.Day.Time,
				CPUMinutes:   cpu.Float64,
				RAMMBMinutes: ram.Float64,
			})
		}
	}

	// Compute today live from snapshots if the range includes today.
	if !to.Before(today) && !from.After(today) {
		todayEnd := today.Add(24 * time.Hour)
		row, err := s.DB.ComputeDailyUsageForDay(ctx, db.ComputeDailyUsageForDayParams{
			TeamID:      teamID,
			SampledAt:   pgtype.Timestamptz{Time: today, Valid: true},
			SampledAt_2: pgtype.Timestamptz{Time: todayEnd, Valid: true},
		})
		if err != nil {
			return nil, fmt.Errorf("compute today usage: %w", err)
		}

		cpu, err := row.CpuMinutes.Float64Value()
		if err != nil {
			return nil, fmt.Errorf("convert today cpu_minutes: %w", err)
		}
		ram, err := row.RamMbMinutes.Float64Value()
		if err != nil {
			return nil, fmt.Errorf("convert today ram_mb_minutes: %w", err)
		}
		points = append(points, UsagePoint{
			Day:          today,
			CPUMinutes:   cpu.Float64,
			RAMMBMinutes: ram.Float64,
		})
	}

	return points, nil
}
