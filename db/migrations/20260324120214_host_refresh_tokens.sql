-- +goose Up

-- Refresh tokens for host agent JWT rotation.
-- Hosts exchange a refresh token for a new short-lived JWT + new refresh token (rotation).
-- Refresh tokens expire after 60 days; hosts must re-register with a new one-time token after that.
CREATE TABLE host_refresh_tokens (
    id         TEXT PRIMARY KEY,
    host_id    TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,   -- SHA-256 hex of the opaque token
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ             -- NULL = active; set on rotation or host delete
);

CREATE INDEX idx_host_refresh_tokens_host ON host_refresh_tokens(host_id);

-- +goose Down

DROP TABLE host_refresh_tokens;
