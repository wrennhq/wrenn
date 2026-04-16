-- +goose Up
ALTER TABLE sandboxes ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}';
ALTER TABLE templates ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}';
ALTER TABLE template_builds ADD COLUMN metadata JSONB NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE sandboxes DROP COLUMN metadata;
ALTER TABLE templates DROP COLUMN metadata;
ALTER TABLE template_builds DROP COLUMN metadata;
