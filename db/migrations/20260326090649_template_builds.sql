-- +goose Up

CREATE TABLE template_builds (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    base_template  TEXT NOT NULL DEFAULT 'minimal',
    recipe         JSONB NOT NULL DEFAULT '[]',
    healthcheck    TEXT,
    vcpus          INTEGER NOT NULL DEFAULT 1,
    memory_mb      INTEGER NOT NULL DEFAULT 512,
    status         TEXT NOT NULL DEFAULT 'pending',
    current_step   INTEGER NOT NULL DEFAULT 0,
    total_steps    INTEGER NOT NULL DEFAULT 0,
    logs           JSONB NOT NULL DEFAULT '[]',
    error          TEXT,
    sandbox_id     TEXT,
    host_id        TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at     TIMESTAMPTZ,
    completed_at   TIMESTAMPTZ
);

-- +goose Down

DROP TABLE template_builds;
