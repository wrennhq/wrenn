-- name: InsertUser :one
INSERT INTO users (id, email, password_hash, name)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: InsertUserOAuth :one
INSERT INTO users (id, email, name)
VALUES ($1, $2, $3)
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

-- name: CountUsers :one
SELECT COUNT(*) FROM users;

-- name: SearchUsersByEmailPrefix :many
SELECT id, email FROM users WHERE email LIKE $1 || '%' ORDER BY email LIMIT 10;

-- name: UpdateUserName :exec
UPDATE users SET name = $2, updated_at = NOW() WHERE id = $1;

-- name: ListUsersAdmin :many
SELECT
    u.id,
    u.email,
    u.name,
    u.is_admin,
    u.is_active,
    u.created_at,
    (SELECT COUNT(*) FROM users_teams ut WHERE ut.user_id = u.id)::int AS teams_joined,
    (SELECT COUNT(*) FROM users_teams ut WHERE ut.user_id = u.id AND ut.role = 'owner')::int AS teams_owned
FROM users u
WHERE u.deleted_at IS NULL
ORDER BY u.created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountUsersAdmin :one
SELECT COUNT(*)::int AS total
FROM users
WHERE deleted_at IS NULL;

-- name: SetUserActive :exec
UPDATE users SET is_active = $2, updated_at = NOW() WHERE id = $1;
