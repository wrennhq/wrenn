-- +goose Up
ALTER TABLE hosts DROP COLUMN mtls_enabled;
ALTER TABLE hosts ADD COLUMN cert_expires_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE hosts DROP COLUMN cert_expires_at;
ALTER TABLE hosts ADD COLUMN mtls_enabled BOOLEAN NOT NULL DEFAULT FALSE;
