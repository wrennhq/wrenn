-- +goose Up
ALTER TABLE users ADD COLUMN status TEXT NOT NULL DEFAULT 'active';

-- Backfill from existing columns.
UPDATE users SET status = 'deleted'  WHERE deleted_at IS NOT NULL;
UPDATE users SET status = 'disabled' WHERE is_active = false AND deleted_at IS NULL;

ALTER TABLE users DROP COLUMN is_active;

-- +goose Down
ALTER TABLE users ADD COLUMN is_active BOOLEAN NOT NULL DEFAULT TRUE;

UPDATE users SET is_active = false WHERE status IN ('inactive', 'disabled', 'deleted');

ALTER TABLE users DROP COLUMN status;
