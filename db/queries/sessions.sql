-- name: InsertSession :one
INSERT INTO sessions (id, user_id, team_id, csrf_token, user_agent, ip_address, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetSession :one
SELECT * FROM sessions WHERE id = $1;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = NOW() WHERE id = $1;

-- name: UpdateSessionTeam :exec
UPDATE sessions SET team_id = $2 WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteSessionForUser :exec
DELETE FROM sessions WHERE id = $1 AND user_id = $2;

-- name: ListSessionsByUserID :many
SELECT * FROM sessions WHERE user_id = $1 ORDER BY last_seen_at DESC;

-- name: DeleteSessionsByUserID :many
DELETE FROM sessions WHERE user_id = $1 RETURNING id;

-- name: DeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at < NOW();
