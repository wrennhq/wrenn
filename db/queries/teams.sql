-- name: InsertTeam :one
INSERT INTO teams (id, name, slug)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetTeam :one
SELECT * FROM teams WHERE id = $1;

-- name: InsertTeamMember :exec
INSERT INTO users_teams (user_id, team_id, is_default, role)
VALUES ($1, $2, $3, $4);

-- name: GetDefaultTeamForUser :one
SELECT t.* FROM teams t
JOIN users_teams ut ON ut.team_id = t.id
WHERE ut.user_id = $1 AND ut.is_default = TRUE AND t.deleted_at IS NULL
LIMIT 1;

-- name: SetTeamBYOC :exec
UPDATE teams SET is_byoc = $2 WHERE id = $1;

-- name: GetBYOCTeams :many
SELECT * FROM teams WHERE is_byoc = TRUE AND deleted_at IS NULL ORDER BY created_at;

-- name: GetTeamMembership :one
SELECT * FROM users_teams WHERE user_id = $1 AND team_id = $2;

-- name: UpdateTeamName :exec
UPDATE teams SET name = $2 WHERE id = $1 AND deleted_at IS NULL;

-- name: SoftDeleteTeam :exec
UPDATE teams SET deleted_at = NOW() WHERE id = $1;

-- name: GetTeamBySlug :one
SELECT * FROM teams WHERE slug = $1 AND deleted_at IS NULL;

-- name: GetTeamsForUser :many
SELECT t.id, t.name, t.slug, t.is_byoc, t.created_at, t.deleted_at, ut.role
FROM teams t
JOIN users_teams ut ON ut.team_id = t.id
WHERE ut.user_id = $1 AND t.deleted_at IS NULL
ORDER BY ut.created_at;

-- name: GetTeamMembers :many
SELECT u.id, u.name, u.email, ut.role, ut.created_at AS joined_at
FROM users_teams ut
JOIN users u ON u.id = ut.user_id
WHERE ut.team_id = $1
ORDER BY ut.created_at;

-- name: UpdateMemberRole :exec
UPDATE users_teams SET role = $3 WHERE team_id = $1 AND user_id = $2;

-- name: DeleteTeamMember :exec
DELETE FROM users_teams WHERE team_id = $1 AND user_id = $2;

-- name: ListTeamsAdmin :many
SELECT
    t.id,
    t.name,
    t.slug,
    t.is_byoc,
    t.created_at,
    t.deleted_at,
    (SELECT COUNT(*) FROM users_teams ut WHERE ut.team_id = t.id)::int AS member_count,
    COALESCE(owner_u.name, '') AS owner_name,
    COALESCE(owner_u.email, '') AS owner_email,
    (SELECT COUNT(*) FROM sandboxes s WHERE s.team_id = t.id AND s.status IN ('running', 'paused', 'starting'))::int AS active_sandbox_count,
    (SELECT COUNT(*) FROM channels c WHERE c.team_id = t.id)::int AS channel_count
FROM teams t
LEFT JOIN users_teams owner_ut ON owner_ut.team_id = t.id AND owner_ut.role = 'owner'
LEFT JOIN users owner_u ON owner_u.id = owner_ut.user_id
WHERE t.id != '00000000-0000-0000-0000-000000000000'
ORDER BY t.deleted_at ASC NULLS FIRST, t.created_at DESC
LIMIT $1 OFFSET $2;

-- name: ListSoleOwnedTeams :many
-- Returns teams where the user is the owner and no other members exist.
SELECT t.id FROM teams t
JOIN users_teams ut ON ut.team_id = t.id
WHERE ut.user_id = $1
  AND ut.role = 'owner'
  AND t.deleted_at IS NULL
  AND NOT EXISTS (
      SELECT 1 FROM users_teams ut2
      WHERE ut2.team_id = t.id AND ut2.user_id <> $1
  );

-- name: CountTeamsAdmin :one
SELECT COUNT(*)::int AS total
FROM teams
WHERE id != '00000000-0000-0000-0000-000000000000';
