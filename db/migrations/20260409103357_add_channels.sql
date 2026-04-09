-- +goose Up

CREATE TABLE channels (
    id          UUID PRIMARY KEY,
    team_id     UUID NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    provider    TEXT NOT NULL,
    config      JSONB NOT NULL DEFAULT '{}',
    event_types TEXT[] NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (team_id, name)
);

CREATE INDEX idx_channels_team ON channels(team_id);

-- +goose Down

DROP TABLE IF EXISTS channels;
