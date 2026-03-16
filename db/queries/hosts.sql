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
SELECT * FROM hosts WHERE team_id = $1 ORDER BY created_at DESC;

-- name: ListHostsByStatus :many
SELECT * FROM hosts WHERE status = $1 ORDER BY created_at DESC;

-- name: RegisterHost :exec
UPDATE hosts
SET arch = $2,
    cpu_cores = $3,
    memory_mb = $4,
    disk_gb = $5,
    address = $6,
    status = 'online',
    last_heartbeat_at = NOW(),
    updated_at = NOW()
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
