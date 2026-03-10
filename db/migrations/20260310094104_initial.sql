-- +goose Up

CREATE TABLE sandboxes (
    id              TEXT PRIMARY KEY,
    owner_id        TEXT NOT NULL DEFAULT '',
    host_id         TEXT NOT NULL DEFAULT 'default',
    template        TEXT NOT NULL DEFAULT 'minimal',
    status          TEXT NOT NULL DEFAULT 'pending',
    vcpus           INTEGER NOT NULL DEFAULT 1,
    memory_mb       INTEGER NOT NULL DEFAULT 512,
    timeout_sec     INTEGER NOT NULL DEFAULT 300,
    guest_ip        TEXT NOT NULL DEFAULT '',
    host_ip         TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMPTZ,
    last_active_at  TIMESTAMPTZ,
    last_updated    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sandboxes_status ON sandboxes(status);
CREATE INDEX idx_sandboxes_host_status ON sandboxes(host_id, status);

-- +goose Down

DROP TABLE sandboxes;
