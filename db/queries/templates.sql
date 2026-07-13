-- name: InsertTemplate :one
INSERT INTO templates (id, name, type, vcpus, memory_mb, size_bytes, team_id, default_user, default_env, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetTemplate :one
SELECT * FROM templates WHERE id = $1;

-- name: GetTemplateByTeam :one
-- Platform templates (team_id = 00000000-...) are visible to all teams.
SELECT * FROM templates WHERE name = $1 AND (team_id = $2 OR team_id = '00000000-0000-0000-0000-000000000000');

-- name: GetTemplateByName :one
-- Look up a template by team_id and name (exact team match, no global fallback).
SELECT * FROM templates WHERE team_id = $1 AND name = $2;

-- name: ResolveTemplateForTeam :one
-- Unqualified reference resolution: prefer the requesting team's own template,
-- then fall back to a platform template of the same name.
SELECT * FROM templates
WHERE name = $1 AND (team_id = $2 OR team_id = '00000000-0000-0000-0000-000000000000')
ORDER BY (team_id = $2) DESC
LIMIT 1;

-- name: GetVisibleTemplateByTeamName :one
-- Slug-qualified reference resolution: the template must belong to the named
-- owning team ($1) and be visible to the requester ($3) — public, owned by the
-- requester, or a platform template.
SELECT * FROM templates
WHERE team_id = $1 AND name = $2
  AND (is_public = TRUE OR team_id = $3 OR team_id = '00000000-0000-0000-0000-000000000000');

-- name: SetTemplateVisibility :one
-- Publish or unpublish a template the team owns. Returns the updated row (or no
-- rows if the team does not own a template of that name).
UPDATE templates SET is_public = $3
WHERE team_id = $1 AND name = $2
RETURNING *;

-- name: ListVisibleTemplates :many
-- Templates the team may launch: its own, platform, and every public template.
-- $2 = type filter ('' = any), $3 = search over name/slug ('' = no search).
SELECT t.*, tm.slug AS team_slug
FROM templates t
JOIN teams tm ON tm.id = t.team_id
WHERE (t.team_id = @team_id OR t.team_id = '00000000-0000-0000-0000-000000000000' OR t.is_public = TRUE)
  AND (@type_filter::text = '' OR t.type = @type_filter::text)
  AND (@search::text = '' OR t.name ILIKE '%' || @search::text || '%' OR tm.slug ILIKE '%' || @search::text || '%')
ORDER BY
  (t.team_id = @team_id) DESC,
  (t.team_id = '00000000-0000-0000-0000-000000000000') DESC,
  t.created_at DESC
LIMIT @row_limit OFFSET @row_offset;

-- name: CountVisibleTemplates :one
SELECT COUNT(*)::int AS total
FROM templates t
JOIN teams tm ON tm.id = t.team_id
WHERE (t.team_id = @team_id OR t.team_id = '00000000-0000-0000-0000-000000000000' OR t.is_public = TRUE)
  AND (@type_filter::text = '' OR t.type = @type_filter::text)
  AND (@search::text = '' OR t.name ILIKE '%' || @search::text || '%' OR tm.slug ILIKE '%' || @search::text || '%');

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

-- name: UpdateTemplateSize :exec
UPDATE templates SET size_bytes = $2 WHERE id = $1;

-- name: ListTemplatesByTeamOnly :many
-- List templates owned by a specific team (NOT including platform templates).
SELECT * FROM templates WHERE team_id = $1 ORDER BY created_at DESC;
