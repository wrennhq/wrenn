-- name: InsertAuditLog :exec
INSERT INTO audit_logs (id, team_id, actor_type, actor_id, actor_name, resource_type, resource_id, action, scope, status, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

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
