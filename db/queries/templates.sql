-- name: InsertTemplate :one
INSERT INTO templates (id, name, type, vcpus, memory_mb, size_bytes, team_id, default_user, default_env)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetTemplate :one
SELECT * FROM templates WHERE id = $1;

-- name: GetTemplateByTeam :one
-- Platform templates (team_id = 00000000-...) are visible to all teams.
SELECT * FROM templates WHERE name = $1 AND (team_id = $2 OR team_id = '00000000-0000-0000-0000-000000000000');

-- name: GetTemplateByName :one
-- Look up a template by team_id and name (exact team match, no global fallback).
SELECT * FROM templates WHERE team_id = $1 AND name = $2;

-- name: GetPlatformTemplateByName :one
-- Check if a global (platform) template exists with the given name.
SELECT * FROM templates WHERE team_id = '00000000-0000-0000-0000-000000000000' AND name = $1;

-- name: ListTemplates :many
SELECT * FROM templates ORDER BY created_at DESC;

-- name: ListTemplatesByType :many
SELECT * FROM templates WHERE type = $1 ORDER BY created_at DESC;

-- name: ListTemplatesByTeam :many
-- Platform templates are visible to all teams.
SELECT * FROM templates WHERE (team_id = $1 OR team_id = '00000000-0000-0000-0000-000000000000') ORDER BY created_at DESC;

-- name: ListTemplatesByTeamAndType :many
-- Platform templates are visible to all teams.
SELECT * FROM templates WHERE (team_id = $1 OR team_id = '00000000-0000-0000-0000-000000000000') AND type = $2 ORDER BY created_at DESC;

-- name: DeleteTemplate :exec
DELETE FROM templates WHERE id = $1;

-- name: DeleteTemplateByTeam :exec
DELETE FROM templates WHERE name = $1 AND team_id = $2;

-- name: DeleteTemplatesByTeam :exec
-- Bulk delete all templates owned by a team (for team soft-delete cleanup).
DELETE FROM templates WHERE team_id = $1;

-- name: ListTemplatesByTeamOnly :many
-- List templates owned by a specific team (NOT including platform templates).
SELECT * FROM templates WHERE team_id = $1 ORDER BY created_at DESC;
