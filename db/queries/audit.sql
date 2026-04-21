-- name: InsertAuditLog :exec
INSERT INTO audit_logs (id, team_id, actor_type, actor_id, actor_name, resource_type, resource_id, action, scope, status, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: AnonymizeAuditLogsByUserID :exec
UPDATE audit_logs
SET actor_name = CASE WHEN actor_id = $1 THEN 'deleted-user' ELSE actor_name END,
    actor_id   = CASE WHEN actor_id = $1 THEN NULL ELSE actor_id END,
    resource_id = CASE WHEN resource_type = 'member' AND resource_id = $1 THEN NULL ELSE resource_id END,
    metadata   = CASE WHEN resource_type = 'member' AND resource_id = $1 AND metadata ? 'email' THEN metadata - 'email' ELSE metadata END
WHERE actor_id = $1
   OR (resource_type = 'member' AND resource_id = $1);

-- name: ListAuditLogs :many
SELECT * FROM audit_logs
WHERE team_id = $1
  AND scope = ANY($2::text[])
  AND (cardinality($3::text[]) = 0 OR resource_type = ANY($3::text[]))
  AND (cardinality($4::text[]) = 0 OR action = ANY($4::text[]))
  AND ($5::timestamptz IS NULL OR created_at < $5
       OR (created_at = $5 AND id < $6))
ORDER BY created_at DESC, id DESC
LIMIT $7;
