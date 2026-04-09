-- name: InsertChannel :one
INSERT INTO channels (id, team_id, name, provider, config, event_types)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListChannelsByTeam :many
SELECT * FROM channels WHERE team_id = $1 ORDER BY created_at DESC;

-- name: GetChannelByTeam :one
SELECT * FROM channels WHERE id = $1 AND team_id = $2;

-- name: UpdateChannel :one
UPDATE channels SET name = $3, event_types = $4, updated_at = NOW()
WHERE id = $1 AND team_id = $2
RETURNING *;

-- name: UpdateChannelConfig :one
UPDATE channels SET config = $3, updated_at = NOW()
WHERE id = $1 AND team_id = $2
RETURNING *;

-- name: DeleteChannelByTeam :exec
DELETE FROM channels WHERE id = $1 AND team_id = $2;

-- name: ListChannelsForEvent :many
SELECT * FROM channels
WHERE team_id = $1
  AND sqlc.arg(event_type)::text = ANY(event_types)
ORDER BY created_at;
