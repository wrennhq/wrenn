-- +goose Up

ALTER TABLE hosts
    ADD COLUMN cert_fingerprint TEXT,
    ADD COLUMN mtls_enabled     BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down

ALTER TABLE hosts
    DROP COLUMN cert_fingerprint,
    DROP COLUMN mtls_enabled;
