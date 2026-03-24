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
