-- +goose Up
ALTER TABLE templates
    ADD COLUMN default_user TEXT NOT NULL DEFAULT 'root',
    ADD COLUMN default_env  JSONB NOT NULL DEFAULT '{}';

ALTER TABLE template_builds
    ADD COLUMN default_user TEXT NOT NULL DEFAULT 'root',
    ADD COLUMN default_env  JSONB NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE template_builds
    DROP COLUMN default_env,
    DROP COLUMN default_user;

ALTER TABLE templates
    DROP COLUMN default_env,
    DROP COLUMN default_user;
