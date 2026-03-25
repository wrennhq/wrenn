-- +goose Up
CREATE TABLE sandbox_metric_points (
    sandbox_id TEXT        NOT NULL,
    tier       TEXT        NOT NULL CHECK (tier IN ('10m', '2h', '24h')),
    ts         BIGINT      NOT NULL,
    cpu_pct    FLOAT8      NOT NULL DEFAULT 0,
    mem_bytes  BIGINT      NOT NULL DEFAULT 0,
    disk_bytes BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (sandbox_id, tier, ts)
);

CREATE INDEX idx_sandbox_metric_points_sandbox_tier
    ON sandbox_metric_points (sandbox_id, tier);

-- +goose Down
DROP TABLE IF EXISTS sandbox_metric_points;
