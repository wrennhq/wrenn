-- +goose Up

ALTER TABLE teams
    ADD COLUMN is_byoc BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down

ALTER TABLE teams
    DROP COLUMN is_byoc;
