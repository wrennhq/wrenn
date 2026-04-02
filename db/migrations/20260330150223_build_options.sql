-- +goose Up

-- Allow completed_at to be set when a build is cancelled.
-- (The UpdateBuildStatus query is updated in sqlc; no schema change needed for that.)

-- Add skip_pre_post flag: when true, the pre-build and post-build command phases
-- are skipped for this build.
ALTER TABLE template_builds ADD COLUMN skip_pre_post BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down
ALTER TABLE template_builds DROP COLUMN skip_pre_post;
