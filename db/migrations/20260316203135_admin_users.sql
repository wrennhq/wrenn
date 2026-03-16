-- +goose Up

ALTER TABLE users
    ADD COLUMN is_admin BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE admin_permissions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    permission TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, permission)
);

CREATE INDEX idx_admin_permissions_user ON admin_permissions(user_id);

-- +goose Down

DROP TABLE admin_permissions;

ALTER TABLE users
    DROP COLUMN is_admin;
