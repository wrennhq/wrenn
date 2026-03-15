-- +goose Up

ALTER TABLE users
    ALTER COLUMN password_hash DROP NOT NULL;

CREATE TABLE oauth_providers (
    provider    TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (provider, provider_id)
);

CREATE INDEX idx_oauth_providers_user ON oauth_providers(user_id);

-- +goose Down

DROP TABLE oauth_providers;

UPDATE users SET password_hash = '' WHERE password_hash IS NULL;
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
