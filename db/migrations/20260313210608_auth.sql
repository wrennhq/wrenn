-- +goose Up

CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE teams (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE users_teams (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    is_default BOOLEAN NOT NULL DEFAULT TRUE,
    role       TEXT NOT NULL DEFAULT 'owner',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (team_id, user_id)
);

CREATE INDEX idx_users_teams_user ON users_teams(user_id);

CREATE TABLE team_api_keys (
    id         TEXT PRIMARY KEY,
    team_id    TEXT NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name       TEXT NOT NULL DEFAULT '',
    key_hash   TEXT NOT NULL UNIQUE,
    key_prefix TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used  TIMESTAMPTZ
);

CREATE INDEX idx_team_api_keys_team ON team_api_keys(team_id);

-- +goose Down

DROP TABLE team_api_keys;
DROP TABLE users_teams;
DROP TABLE teams;
DROP TABLE users;
