-- name: InsertSandbox :one
INSERT INTO sandboxes (id, team_id, host_id, template, status, vcpus, memory_mb, timeout_sec, disk_size_mb, template_id, template_team_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetSandbox :one
SELECT * FROM sandboxes WHERE id = $1;

-- name: GetSandboxByTeam :one
SELECT * FROM sandboxes WHERE id = $1 AND team_id = $2;

-- name: ListSandboxes :many
SELECT * FROM sandboxes ORDER BY created_at DESC;

-- name: ListSandboxesByTeam :many
SELECT * FROM sandboxes
WHERE team_id = $1 AND status NOT IN ('stopped', 'error')
ORDER BY created_at DESC;

-- name: ListSandboxesByHostAndStatus :many
SELECT * FROM sandboxes
WHERE host_id = $1 AND status = ANY($2::text[])
ORDER BY created_at DESC;

-- name: UpdateSandboxRunning :one
UPDATE sandboxes
SET status = 'running',
    host_ip = $2,
    guest_ip = $3,
    started_at = $4,
    last_active_at = $4,
    last_updated = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateSandboxStatus :one
UPDATE sandboxes
SET status = $2,
    last_updated = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateLastActive :exec
UPDATE sandboxes
SET last_active_at = $2,
    last_updated = NOW()
WHERE id = $1;

-- name: BulkUpdateStatusByIDs :exec
UPDATE sandboxes
SET status = $2,
    last_updated = NOW()
WHERE id = ANY($1::uuid[]);

-- name: ListActiveSandboxesByTeam :many
SELECT * FROM sandboxes
WHERE team_id = $1 AND status IN ('running', 'paused', 'starting')
ORDER BY created_at DESC;

-- name: MarkSandboxesMissingByHost :exec
-- Called when the host monitor marks a host unreachable.
-- Marks running/starting/pending sandboxes on that host as 'missing' so users see
-- the sandbox is not currently reachable, without permanently losing the record.
UPDATE sandboxes
SET status       = 'missing',
    last_updated = NOW()
WHERE host_id = $1 AND status IN ('running', 'starting', 'pending');

-- name: BulkRestoreRunning :exec
-- Called by the reconciler when a host comes back online and its sandboxes are
-- confirmed alive. Restores only sandboxes that are in 'missing' state.
UPDATE sandboxes
SET status       = 'running',
    last_updated = NOW()
WHERE id = ANY($1::uuid[]) AND status = 'missing';
