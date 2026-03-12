-- +goose Up

CREATE TABLE templates (
    name         TEXT PRIMARY KEY,
    type         TEXT NOT NULL DEFAULT 'base',        -- 'base' or 'snapshot'
    vcpus        INTEGER,
    memory_mb    INTEGER,
    size_bytes   BIGINT NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down

DROP TABLE templates;
