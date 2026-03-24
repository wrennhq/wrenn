-- name: InsertHostRefreshToken :one
INSERT INTO host_refresh_tokens (id, host_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetHostRefreshTokenByHash :one
SELECT * FROM host_refresh_tokens
WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > NOW();

-- name: RevokeHostRefreshToken :exec
UPDATE host_refresh_tokens SET revoked_at = NOW() WHERE id = $1;

-- name: RevokeHostRefreshTokensByHost :exec
UPDATE host_refresh_tokens SET revoked_at = NOW()
WHERE host_id = $1 AND revoked_at IS NULL;

-- name: DeleteExpiredHostRefreshTokens :exec
DELETE FROM host_refresh_tokens
WHERE expires_at < NOW() OR revoked_at IS NOT NULL;
