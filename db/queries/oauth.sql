-- name: InsertOAuthProvider :exec
INSERT INTO oauth_providers (provider, provider_id, user_id, email)
VALUES ($1, $2, $3, $4);

-- name: GetOAuthProvider :one
SELECT * FROM oauth_providers
WHERE provider = $1 AND provider_id = $2;

-- name: GetOAuthProvidersByUserID :many
SELECT * FROM oauth_providers WHERE user_id = $1;

-- name: DeleteOAuthProvider :exec
DELETE FROM oauth_providers WHERE user_id = $1 AND provider = $2;
