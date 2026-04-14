-- +goose Up
ALTER TABLE users ADD COLUMN is_active BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE users ADD COLUMN deleted_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE users DROP COLUMN deleted_at;
ALTER TABLE users DROP COLUMN is_active;
