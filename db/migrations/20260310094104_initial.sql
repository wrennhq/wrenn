-- +goose Up

-- teams
CREATE TABLE teams (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL,
    slug       TEXT NOT NULL UNIQUE,
    is_byoc    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);
CREATE INDEX idx_teams_slug ON teams(slug);

-- users
CREATE TABLE users (
    id            UUID PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT,
    name          TEXT NOT NULL DEFAULT '',
    is_admin      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- users_teams (junction)
CREATE TABLE users_teams (
    user_id    UUID NOT NULL REFERENCES users(id),
    team_id    UUID NOT NULL REFERENCES teams(id),
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    role       TEXT NOT NULL DEFAULT 'member',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX idx_users_teams_user ON users_teams(user_id);

-- team_api_keys
CREATE TABLE team_api_keys (
    id         UUID PRIMARY KEY,
    team_id    UUID NOT NULL REFERENCES teams(id),
    name       TEXT NOT NULL,
    key_hash   TEXT NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL,
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used  TIMESTAMPTZ
);
CREATE INDEX idx_team_api_keys_team ON team_api_keys(team_id);

-- oauth_providers
CREATE TABLE oauth_providers (
    provider    TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    user_id     UUID NOT NULL REFERENCES users(id),
    email       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (provider, provider_id)
);
CREATE INDEX idx_oauth_providers_user ON oauth_providers(user_id);

-- admin_permissions
CREATE TABLE admin_permissions (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id),
    permission TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, permission)
);
CREATE INDEX idx_admin_permissions_user ON admin_permissions(user_id);

-- hosts
CREATE TABLE hosts (
    id                UUID PRIMARY KEY,
    type              TEXT NOT NULL DEFAULT 'regular',
    team_id           UUID REFERENCES teams(id),
    provider          TEXT NOT NULL DEFAULT '',
    availability_zone TEXT NOT NULL DEFAULT '',
    arch              TEXT NOT NULL DEFAULT '',
    cpu_cores         INTEGER NOT NULL DEFAULT 0,
    memory_mb         INTEGER NOT NULL DEFAULT 0,
    disk_gb           INTEGER NOT NULL DEFAULT 0,
    address           TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'pending',
    last_heartbeat_at TIMESTAMPTZ,
    metadata          JSONB NOT NULL DEFAULT '{}',
    created_by        UUID NOT NULL REFERENCES users(id),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    cert_fingerprint  TEXT NOT NULL DEFAULT '',
    mtls_enabled      BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE INDEX idx_hosts_type ON hosts(type);
CREATE INDEX idx_hosts_team ON hosts(team_id);
CREATE INDEX idx_hosts_status ON hosts(status);

-- host_tokens
CREATE TABLE host_tokens (
    id         UUID PRIMARY KEY,
    host_id    UUID NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);
CREATE INDEX idx_host_tokens_host ON host_tokens(host_id);

-- host_tags
CREATE TABLE host_tags (
    host_id UUID NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    tag     TEXT NOT NULL,
    PRIMARY KEY (host_id, tag)
);
CREATE INDEX idx_host_tags_tag ON host_tags(tag);

-- host_refresh_tokens
CREATE TABLE host_refresh_tokens (
    id         UUID PRIMARY KEY,
    host_id    UUID NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMPTZ
);
CREATE INDEX idx_host_refresh_tokens_host ON host_refresh_tokens(host_id);

-- templates (TEXT primary key — not UUID)
CREATE TABLE templates (
    name       TEXT PRIMARY KEY,
    type       TEXT NOT NULL DEFAULT 'base',
    vcpus      INTEGER NOT NULL DEFAULT 1,
    memory_mb  INTEGER NOT NULL DEFAULT 512,
    size_bytes BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    team_id    UUID NOT NULL
);
CREATE INDEX idx_templates_team ON templates(team_id);

-- sandboxes
CREATE TABLE sandboxes (
    id             UUID PRIMARY KEY,
    team_id        UUID NOT NULL REFERENCES teams(id),
    host_id        UUID NOT NULL,
    template       TEXT NOT NULL DEFAULT 'minimal',
    status         TEXT NOT NULL DEFAULT 'pending',
    vcpus          INTEGER NOT NULL DEFAULT 1,
    memory_mb      INTEGER NOT NULL DEFAULT 512,
    timeout_sec    INTEGER NOT NULL DEFAULT 300,
    guest_ip       TEXT NOT NULL DEFAULT '',
    host_ip        TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at     TIMESTAMPTZ,
    last_active_at TIMESTAMPTZ,
    last_updated   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_sandboxes_status ON sandboxes(status);
CREATE INDEX idx_sandboxes_host_status ON sandboxes(host_id, status);
CREATE INDEX idx_sandboxes_team ON sandboxes(team_id);

-- audit_logs (id and team_id are UUID; actor_id and resource_id are TEXT for polymorphism)
CREATE TABLE audit_logs (
    id            UUID PRIMARY KEY,
    team_id       UUID NOT NULL,
    actor_type    TEXT NOT NULL,
    actor_id      TEXT,
    actor_name    TEXT NOT NULL DEFAULT '',
    resource_type TEXT NOT NULL,
    resource_id   TEXT,
    action        TEXT NOT NULL,
    scope         TEXT NOT NULL DEFAULT 'team',
    status        TEXT NOT NULL DEFAULT 'success',
    metadata      JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_audit_logs_team_time ON audit_logs(team_id, created_at DESC);
CREATE INDEX idx_audit_logs_team_resource ON audit_logs(team_id, resource_type, created_at DESC);

-- sandbox_metrics_snapshots
CREATE TABLE sandbox_metrics_snapshots (
    id                 BIGSERIAL PRIMARY KEY,
    team_id            UUID NOT NULL,
    sampled_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    running_count      INTEGER NOT NULL DEFAULT 0,
    vcpus_reserved     INTEGER NOT NULL DEFAULT 0,
    memory_mb_reserved INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_metrics_snapshots_team_time ON sandbox_metrics_snapshots(team_id, sampled_at DESC);

-- sandbox_metric_points
CREATE TABLE sandbox_metric_points (
    sandbox_id UUID NOT NULL,
    tier       TEXT NOT NULL CHECK (tier IN ('10m', '2h', '24h')),
    ts         BIGINT NOT NULL,
    cpu_pct    FLOAT8 NOT NULL DEFAULT 0,
    mem_bytes  BIGINT NOT NULL DEFAULT 0,
    disk_bytes BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (sandbox_id, tier, ts)
);
CREATE INDEX idx_sandbox_metric_points_sandbox_tier ON sandbox_metric_points(sandbox_id, tier);

-- template_builds
CREATE TABLE template_builds (
    id            UUID PRIMARY KEY,
    name          TEXT NOT NULL,
    base_template TEXT NOT NULL,
    recipe        JSONB NOT NULL DEFAULT '[]',
    healthcheck   TEXT NOT NULL DEFAULT '',
    vcpus         INTEGER NOT NULL DEFAULT 1,
    memory_mb     INTEGER NOT NULL DEFAULT 512,
    status        TEXT NOT NULL DEFAULT 'pending',
    current_step  INTEGER NOT NULL DEFAULT 0,
    total_steps   INTEGER NOT NULL DEFAULT 0,
    logs          JSONB NOT NULL DEFAULT '[]',
    error         TEXT NOT NULL DEFAULT '',
    sandbox_id    UUID,
    host_id       UUID,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at    TIMESTAMPTZ,
    completed_at  TIMESTAMPTZ
);

-- +goose Down
DROP TABLE IF EXISTS template_builds;
DROP TABLE IF EXISTS sandbox_metric_points;
DROP TABLE IF EXISTS sandbox_metrics_snapshots;
DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS sandboxes;
DROP TABLE IF EXISTS templates;
DROP TABLE IF EXISTS host_refresh_tokens;
DROP TABLE IF EXISTS host_tags;
DROP TABLE IF EXISTS host_tokens;
DROP TABLE IF EXISTS hosts;
DROP TABLE IF EXISTS admin_permissions;
DROP TABLE IF EXISTS oauth_providers;
DROP TABLE IF EXISTS team_api_keys;
DROP TABLE IF EXISTS users_teams;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS teams;
