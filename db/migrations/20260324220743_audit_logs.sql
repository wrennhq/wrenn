-- +goose Up

CREATE TABLE audit_logs (
    id            TEXT        PRIMARY KEY,
    team_id       TEXT        NOT NULL,
    actor_type    TEXT        NOT NULL,   -- 'user', 'api_key', 'system'
    actor_id      TEXT,                   -- user_id or api_key_id; NULL for system
    actor_name    TEXT,                   -- display name snapshotted at write time; NULL for system
    resource_type TEXT        NOT NULL,   -- 'sandbox', 'snapshot', 'team', 'api_key', 'member', 'host'
    resource_id   TEXT,                   -- primary ID of the affected resource; NULL when not applicable
    action        TEXT        NOT NULL,   -- 'create', 'pause', 'resume', 'destroy', 'delete', 'rename',
                                          -- 'revoke', 'add', 'remove', 'leave', 'role_update',
                                          -- 'marked_down', 'marked_up'
    scope         TEXT        NOT NULL,   -- 'team' or 'admin'
    status        TEXT        NOT NULL,   -- 'success', 'info', 'warning', 'error'
    metadata      JSONB       NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Primary access pattern: team feed sorted newest-first with cursor pagination.
CREATE INDEX idx_audit_logs_team_time ON audit_logs (team_id, created_at DESC);

-- Secondary index: filtered by resource_type and action within a team.
CREATE INDEX idx_audit_logs_team_resource ON audit_logs (team_id, resource_type, action, created_at DESC);

-- +goose Down

DROP TABLE audit_logs;
