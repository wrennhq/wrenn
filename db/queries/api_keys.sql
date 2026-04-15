-- name: InsertAPIKey :one
INSERT INTO team_api_keys (id, team_id, name, key_hash, key_prefix, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetAPIKeyByHash :one
SELECT * FROM team_api_keys WHERE key_hash = $1;

-- name: ListAPIKeysByTeam :many
SELECT * FROM team_api_keys WHERE team_id = $1 ORDER BY created_at DESC;

-- name: ListAPIKeysByTeamWithCreator :many
SELECT k.id, k.team_id, k.name, k.key_hash, k.key_prefix, k.created_by, k.created_at, k.last_used,
       u.email AS creator_email
FROM team_api_keys k
LEFT JOIN users u ON u.id = k.created_by
WHERE k.team_id = $1
ORDER BY k.created_at DESC;

-- name: DeleteAPIKey :exec
DELETE FROM team_api_keys WHERE id = $1 AND team_id = $2;

-- name: UpdateAPIKeyLastUsed :exec
UPDATE team_api_keys SET last_used = NOW() WHERE id = $1;

-- name: DeleteAPIKeysByTeam :exec
DELETE FROM team_api_keys WHERE team_id = $1;
