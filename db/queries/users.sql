-- name: InsertUser :one
INSERT INTO users (id, email, password_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: InsertUserOAuth :one
INSERT INTO users (id, email)
VALUES ($1, $2)
RETURNING *;

-- name: SetUserAdmin :exec
UPDATE users SET is_admin = $2, updated_at = NOW() WHERE id = $1;

-- name: GetAdminUsers :many
SELECT * FROM users WHERE is_admin = TRUE ORDER BY created_at;

-- name: InsertAdminPermission :exec
INSERT INTO admin_permissions (id, user_id, permission)
VALUES ($1, $2, $3);

-- name: DeleteAdminPermission :exec
DELETE FROM admin_permissions WHERE user_id = $1 AND permission = $2;

-- name: GetAdminPermissions :many
SELECT * FROM admin_permissions WHERE user_id = $1 ORDER BY permission;

-- name: HasAdminPermission :one
SELECT EXISTS(
    SELECT 1 FROM admin_permissions WHERE user_id = $1 AND permission = $2
) AS has_permission;
