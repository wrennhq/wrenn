-- +goose Up
CREATE TABLE daily_usage (
    team_id        UUID NOT NULL,
    day            DATE NOT NULL,
    cpu_minutes    NUMERIC(18, 4) NOT NULL DEFAULT 0,
    ram_mb_minutes NUMERIC(18, 4) NOT NULL DEFAULT 0,
    PRIMARY KEY (team_id, day)
);

-- +goose Down
DROP TABLE daily_usage;
