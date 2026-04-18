-- name: InsertMetricsSnapshot :exec
INSERT INTO sandbox_metrics_snapshots (team_id, running_count, vcpus_reserved, memory_mb_reserved)
VALUES ($1, $2, $3, $4);

-- name: GetLiveMetrics :one
-- Reads directly from sandboxes for accurate real-time current values.
-- CPU reserved = running + starting only (paused VMs release CPU).
-- RAM reserved = running + starting + sum(ceil(each_paused/2)) (per-VM ceiling).
SELECT
    (COUNT(*) FILTER (WHERE status IN ('running', 'starting')))::INTEGER                                              AS running_count,
    (COALESCE(SUM(vcpus)     FILTER (WHERE status IN ('running', 'starting')), 0))::INTEGER                          AS vcpus_reserved,
    (COALESCE(SUM(memory_mb) FILTER (WHERE status IN ('running', 'starting')), 0)
     + COALESCE(SUM(CEIL(memory_mb::NUMERIC / 2)) FILTER (WHERE status = 'paused'), 0))::INTEGER                     AS memory_mb_reserved
FROM sandboxes
WHERE team_id = $1;

-- name: GetPeakMetrics :one
SELECT
    COALESCE(MAX(running_count), 0)::INTEGER      AS peak_running_count,
    COALESCE(MAX(vcpus_reserved), 0)::INTEGER     AS peak_vcpus,
    COALESCE(MAX(memory_mb_reserved), 0)::INTEGER AS peak_memory_mb
FROM sandbox_metrics_snapshots
WHERE team_id = $1
  AND sampled_at > NOW() - INTERVAL '30 days';

-- name: PruneOldMetrics :exec
DELETE FROM sandbox_metrics_snapshots
WHERE sampled_at < NOW() - INTERVAL '60 days';

-- name: InsertSandboxMetricPoint :exec
INSERT INTO sandbox_metric_points (sandbox_id, tier, ts, cpu_pct, mem_bytes, disk_bytes)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (sandbox_id, tier, ts) DO NOTHING;

-- name: GetSandboxMetricPoints :many
SELECT ts, cpu_pct, mem_bytes, disk_bytes
FROM sandbox_metric_points
WHERE sandbox_id = $1 AND tier = $2 AND ts >= $3
ORDER BY ts ASC;

-- name: DeleteSandboxMetricPoints :exec
DELETE FROM sandbox_metric_points
WHERE sandbox_id = $1;

-- name: DeleteSandboxMetricPointsByTier :exec
DELETE FROM sandbox_metric_points
WHERE sandbox_id = $1 AND tier = $2;

-- name: PruneSandboxMetricPoints :exec
-- Remove metric points older than 30 days for destroyed sandboxes.
DELETE FROM sandbox_metric_points
WHERE ts < EXTRACT(EPOCH FROM NOW() - INTERVAL '30 days')::BIGINT;

-- name: DeleteMetricsSnapshotsByTeam :exec
DELETE FROM sandbox_metrics_snapshots WHERE team_id = $1;

-- name: DeleteMetricPointsByTeam :exec
DELETE FROM sandbox_metric_points
WHERE sandbox_id IN (SELECT id FROM sandboxes WHERE team_id = $1);

-- name: SampleSandboxMetrics :many
-- Aggregates per-team resource usage from the live sandboxes table.
-- Groups by all teams that have any sandbox row (including stopped) so that
-- zero-value snapshots are recorded when all capsules are stopped, keeping the
-- time-series charts continuous rather than trailing off into empty space.
-- CPU reserved = running + starting only (paused VMs release CPU).
-- RAM reserved = running + starting + sum(ceil(each_paused/2)) (per-VM ceiling).
SELECT
    team_id,
    (COUNT(*) FILTER (WHERE status IN ('running', 'starting')))::INTEGER                                              AS running_count,
    (COALESCE(SUM(vcpus)     FILTER (WHERE status IN ('running', 'starting')), 0))::INTEGER                          AS vcpus_reserved,
    (COALESCE(SUM(memory_mb) FILTER (WHERE status IN ('running', 'starting')), 0)
     + COALESCE(SUM(CEIL(memory_mb::NUMERIC / 2)) FILTER (WHERE status = 'paused'), 0))::INTEGER                     AS memory_mb_reserved
FROM sandboxes
GROUP BY team_id;

-- name: GetTeamsWithSnapshots :many
SELECT DISTINCT team_id
FROM sandbox_metrics_snapshots
WHERE sampled_at > NOW() - INTERVAL '93 days';

-- name: ComputeDailyUsageForDay :one
SELECT
    COALESCE(SUM(vcpus_reserved     * 10.0 / 60.0), 0)::NUMERIC(18,4) AS cpu_minutes,
    COALESCE(SUM(memory_mb_reserved * 10.0 / 60.0), 0)::NUMERIC(18,4) AS ram_mb_minutes
FROM sandbox_metrics_snapshots
WHERE team_id    = $1
  AND sampled_at >= $2
  AND sampled_at <  $3;

-- name: UpsertDailyUsage :exec
INSERT INTO daily_usage (team_id, day, cpu_minutes, ram_mb_minutes)
VALUES ($1, $2, $3, $4)
ON CONFLICT (team_id, day) DO UPDATE
    SET cpu_minutes    = EXCLUDED.cpu_minutes,
        ram_mb_minutes = EXCLUDED.ram_mb_minutes;

-- name: GetDailyUsage :many
SELECT day, cpu_minutes, ram_mb_minutes
FROM daily_usage
WHERE team_id = $1
  AND day >= $2
  AND day <= $3
ORDER BY day ASC;

-- name: DeleteDailyUsageByTeam :exec
DELETE FROM daily_usage WHERE team_id = $1;
