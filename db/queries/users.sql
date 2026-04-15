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

-- name: InsertUserInactive :one
INSERT INTO users (id, email, password_hash, name, status)
VALUES ($1, $2, $3, $4, 'inactive')
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

-- name: CountActiveUsers :one
SELECT COUNT(*) FROM users WHERE status = 'active';

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
    u.status,
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

-- name: SetUserStatus :exec
UPDATE users SET status = $2, updated_at = NOW() WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = NOW() WHERE id = $1;

-- name: SoftDeleteUser :exec
UPDATE users SET deleted_at = NOW(), status = 'deleted', updated_at = NOW() WHERE id = $1;

-- name: CountUserOwnedTeamsWithOtherMembers :one
SELECT COUNT(DISTINCT ut.team_id)::int
FROM users_teams ut
WHERE ut.user_id = $1
  AND ut.role = 'owner'
  AND EXISTS (
      SELECT 1 FROM users_teams ut2
      WHERE ut2.team_id = ut.team_id AND ut2.user_id <> $1
  );

-- name: HardDeleteExpiredUsers :exec
DELETE FROM users WHERE deleted_at IS NOT NULL AND deleted_at < NOW() - INTERVAL '15 days';

-- name: HardDeleteUser :exec
DELETE FROM users WHERE id = $1;
