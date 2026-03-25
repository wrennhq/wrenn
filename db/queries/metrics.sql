-- name: InsertMetricsSnapshot :exec
INSERT INTO sandbox_metrics_snapshots (team_id, running_count, vcpus_reserved, memory_mb_reserved)
VALUES ($1, $2, $3, $4);

-- name: GetLiveMetrics :one
-- Reads directly from sandboxes for accurate real-time current values.
-- CPU reserved = running + starting only (paused VMs release CPU).
-- RAM reserved = running + starting + ceil(paused/2) (capacity held for resume).
SELECT
    (COUNT(*) FILTER (WHERE status IN ('running', 'starting')))::INTEGER                                              AS running_count,
    (COALESCE(SUM(vcpus)     FILTER (WHERE status IN ('running', 'starting')), 0))::INTEGER                          AS vcpus_reserved,
    (COALESCE(SUM(memory_mb) FILTER (WHERE status IN ('running', 'starting')), 0)
     + CEIL(COALESCE(SUM(memory_mb) FILTER (WHERE status = 'paused'), 0)::NUMERIC / 2))::INTEGER                     AS memory_mb_reserved
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

-- name: SampleSandboxMetrics :many
-- Aggregates per-team resource usage from the live sandboxes table.
-- CPU reserved = running + starting only (paused VMs release CPU).
-- RAM reserved = running + starting + ceil(paused/2) (capacity held for resume).
SELECT
    team_id,
    (COUNT(*) FILTER (WHERE status IN ('running', 'starting')))::INTEGER                                              AS running_count,
    (COALESCE(SUM(vcpus)     FILTER (WHERE status IN ('running', 'starting')), 0))::INTEGER                          AS vcpus_reserved,
    (COALESCE(SUM(memory_mb) FILTER (WHERE status IN ('running', 'starting')), 0)
     + CEIL(COALESCE(SUM(memory_mb) FILTER (WHERE status = 'paused'), 0)::NUMERIC / 2))::INTEGER                     AS memory_mb_reserved
FROM sandboxes
WHERE status IN ('running', 'starting', 'paused')
GROUP BY team_id;
