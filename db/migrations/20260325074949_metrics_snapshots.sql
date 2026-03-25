-- +goose Up

CREATE TABLE sandbox_metrics_snapshots (
    id                 BIGSERIAL    PRIMARY KEY,
    team_id            TEXT         NOT NULL,
    sampled_at         TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    running_count      INTEGER      NOT NULL,
    vcpus_reserved     INTEGER      NOT NULL,
    memory_mb_reserved INTEGER      NOT NULL
);

-- All queries filter on team_id first then range-scan sampled_at.
CREATE INDEX idx_metrics_snapshots_team_time
    ON sandbox_metrics_snapshots (team_id, sampled_at DESC);

-- +goose Down

DROP TABLE sandbox_metrics_snapshots;
