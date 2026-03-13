-- name: InsertTeam :one
INSERT INTO teams (id, name)
VALUES ($1, $2)
RETURNING *;

-- name: GetTeam :one
SELECT * FROM teams WHERE id = $1;

-- name: InsertTeamMember :exec
INSERT INTO users_teams (user_id, team_id, is_default, role)
VALUES ($1, $2, $3, $4);

-- name: GetDefaultTeamForUser :one
SELECT t.* FROM teams t
JOIN users_teams ut ON ut.team_id = t.id
WHERE ut.user_id = $1 AND ut.is_default = TRUE
LIMIT 1;
