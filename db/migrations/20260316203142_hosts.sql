-- +goose Up

CREATE TABLE hosts (
    id                TEXT PRIMARY KEY,
    type              TEXT NOT NULL DEFAULT 'regular',  -- 'regular' or 'byoc'
    team_id           TEXT REFERENCES teams(id) ON DELETE SET NULL,
    provider          TEXT,
    availability_zone TEXT,
    arch              TEXT,
    cpu_cores         INTEGER,
    memory_mb         INTEGER,
    disk_gb           INTEGER,
    address           TEXT,                             -- ip:port of host agent
    status            TEXT NOT NULL DEFAULT 'pending',  -- 'pending', 'online', 'offline', 'draining'
    last_heartbeat_at TIMESTAMPTZ,
    metadata          JSONB NOT NULL DEFAULT '{}',
    created_by        TEXT NOT NULL REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE host_tokens (
    id         TEXT PRIMARY KEY,
    host_id    TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    created_by TEXT NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);

CREATE TABLE host_tags (
    host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    tag     TEXT NOT NULL,
    PRIMARY KEY (host_id, tag)
);

CREATE INDEX idx_hosts_type ON hosts(type);
CREATE INDEX idx_hosts_team ON hosts(team_id);
CREATE INDEX idx_hosts_status ON hosts(status);
CREATE INDEX idx_host_tokens_host ON host_tokens(host_id);
CREATE INDEX idx_host_tags_tag ON host_tags(tag);

-- +goose Down

DROP TABLE host_tags;
DROP TABLE host_tokens;
DROP TABLE hosts;
