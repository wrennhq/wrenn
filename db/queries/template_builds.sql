-- name: InsertTemplateBuild :one
INSERT INTO template_builds (id, name, base_template, recipe, healthcheck, vcpus, memory_mb, status, total_steps, template_id, team_id, skip_pre_post)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $9, $10, $11)
RETURNING *;

-- name: GetTemplateBuild :one
SELECT * FROM template_builds WHERE id = $1;

-- name: ListTemplateBuilds :many
SELECT * FROM template_builds ORDER BY created_at DESC;

-- name: UpdateBuildStatus :one
UPDATE template_builds
SET status = $2,
    started_at   = CASE WHEN $2 = 'running'   AND started_at   IS NULL THEN NOW() ELSE started_at   END,
    completed_at = CASE WHEN $2 IN ('success', 'failed', 'cancelled') THEN NOW() ELSE completed_at END
WHERE id = $1
RETURNING *;

-- name: UpdateBuildProgress :exec
UPDATE template_builds
SET current_step = $2, logs = $3
WHERE id = $1;

-- name: UpdateBuildSandbox :exec
UPDATE template_builds
SET sandbox_id = $2, host_id = $3
WHERE id = $1;

-- name: UpdateBuildError :exec
UPDATE template_builds
SET error = $2, status = 'failed', completed_at = NOW()
WHERE id = $1;

-- name: UpdateBuildDefaults :exec
UPDATE template_builds
SET default_user = $2, default_env = $3
WHERE id = $1;
