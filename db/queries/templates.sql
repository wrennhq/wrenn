-- name: InsertTemplate :one
INSERT INTO templates (name, type, vcpus, memory_mb, size_bytes)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetTemplate :one
SELECT * FROM templates WHERE name = $1;

-- name: ListTemplates :many
SELECT * FROM templates ORDER BY created_at DESC;

-- name: ListTemplatesByType :many
SELECT * FROM templates WHERE type = $1 ORDER BY created_at DESC;

-- name: DeleteTemplate :exec
DELETE FROM templates WHERE name = $1;
