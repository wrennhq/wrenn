-- +goose Up
ALTER TABLE users ADD COLUMN name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN name;
