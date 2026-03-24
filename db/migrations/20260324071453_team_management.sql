-- +goose Up

ALTER TABLE teams ADD COLUMN slug TEXT;
ALTER TABLE teams ADD COLUMN deleted_at TIMESTAMPTZ;

-- Backfill slugs for existing teams using MD5 of their ID.
-- MD5 returns 32 hex chars; take chars 1-6 and 7-12 to form a 6-6 slug.
UPDATE teams SET slug = LEFT(MD5(id), 6) || '-' || SUBSTRING(MD5(id), 7, 6);

ALTER TABLE teams ALTER COLUMN slug SET NOT NULL;
CREATE UNIQUE INDEX idx_teams_slug ON teams(slug);

-- +goose Down

DROP INDEX idx_teams_slug;
ALTER TABLE teams DROP COLUMN deleted_at;
ALTER TABLE teams DROP COLUMN slug;
