-- name: InsertHost :one
INSERT INTO hosts (id, type, team_id, provider, availability_zone, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetHost :one
SELECT * FROM hosts WHERE id = $1;

-- name: ListHosts :many
SELECT * FROM hosts ORDER BY created_at DESC;

-- name: ListHostsByType :many
SELECT * FROM hosts WHERE type = $1 ORDER BY created_at DESC;

-- name: ListHostsByTeam :many
SELECT * FROM hosts WHERE team_id = $1 AND type = 'byoc' ORDER BY created_at DESC;

-- name: ListHostsByStatus :many
SELECT * FROM hosts WHERE status = $1 ORDER BY created_at DESC;

-- name: RegisterHost :execrows
UPDATE hosts
SET arch             = $2,
    cpu_cores        = $3,
    memory_mb        = $4,
    disk_gb          = $5,
    address          = $6,
    cert_fingerprint = $7,
    cert_expires_at  = $8,
    status           = 'online',
    last_heartbeat_at = NOW(),
    updated_at        = NOW()
WHERE id = $1 AND status = 'pending';

-- name: UpdateHostCert :exec
UPDATE hosts
SET cert_fingerprint = $2,
    cert_expires_at  = $3,
    updated_at       = NOW()
WHERE id = $1;

-- name: UpdateHostStatus :exec
UPDATE hosts SET status = $2, updated_at = NOW() WHERE id = $1;

-- name: UpdateHostHeartbeat :exec
UPDATE hosts SET last_heartbeat_at = NOW(), updated_at = NOW() WHERE id = $1;

-- name: DeleteHost :exec
DELETE FROM hosts WHERE id = $1;

-- name: AddHostTag :exec
INSERT INTO host_tags (host_id, tag) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: RemoveHostTag :exec
DELETE FROM host_tags WHERE host_id = $1 AND tag = $2;

-- name: GetHostTags :many
SELECT tag FROM host_tags WHERE host_id = $1 ORDER BY tag;

-- name: ListHostsByTag :many
SELECT h.* FROM hosts h
JOIN host_tags ht ON ht.host_id = h.id
WHERE ht.tag = $1
ORDER BY h.created_at DESC;

-- name: InsertHostToken :one
INSERT INTO host_tokens (id, host_id, created_by, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: MarkHostTokenUsed :exec
UPDATE host_tokens SET used_at = NOW() WHERE id = $1;

-- name: GetHostTokensByHost :many
SELECT * FROM host_tokens WHERE host_id = $1 ORDER BY created_at DESC;

-- name: GetHostByTeam :one
SELECT * FROM hosts WHERE id = $1 AND team_id = $2;

-- name: ListActiveHosts :many
-- Returns all hosts that have completed registration (not pending/offline).
SELECT * FROM hosts WHERE status NOT IN ('pending', 'offline') ORDER BY created_at;

-- name: UpdateHostHeartbeatAndStatus :execrows
-- Updates last_heartbeat_at and transitions unreachable hosts back to online.
-- Returns 0 if no host was found (deleted), which the caller treats as 404.
UPDATE hosts
SET last_heartbeat_at = NOW(),
    status            = CASE WHEN status = 'unreachable' THEN 'online' ELSE status END,
    updated_at        = NOW()
WHERE id = $1;

-- name: MarkHostUnreachable :exec
UPDATE hosts SET status = 'unreachable', updated_at = NOW() WHERE id = $1;
