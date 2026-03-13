-- name: InsertTemplate :one
INSERT INTO templates (name, type, vcpus, memory_mb, size_bytes, team_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetTemplate :one
SELECT * FROM templates WHERE name = $1;

-- name: GetTemplateByTeam :one
SELECT * FROM templates WHERE name = $1 AND team_id = $2;

-- name: ListTemplates :many
SELECT * FROM templates ORDER BY created_at DESC;

-- name: ListTemplatesByType :many
SELECT * FROM templates WHERE type = $1 ORDER BY created_at DESC;

-- name: ListTemplatesByTeam :many
SELECT * FROM templates WHERE team_id = $1 ORDER BY created_at DESC;

-- name: ListTemplatesByTeamAndType :many
SELECT * FROM templates WHERE team_id = $1 AND type = $2 ORDER BY created_at DESC;

-- name: DeleteTemplate :exec
DELETE FROM templates WHERE name = $1;

-- name: DeleteTemplateByTeam :exec
DELETE FROM templates WHERE name = $1 AND team_id = $2;
