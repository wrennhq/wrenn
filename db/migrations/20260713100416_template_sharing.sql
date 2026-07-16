-- +goose Up

-- Templates can be published so teams other than the owner can launch them.
-- Owners reference foreign public templates as "<team-slug>/<name>".
ALTER TABLE templates ADD COLUMN is_public BOOLEAN NOT NULL DEFAULT FALSE;

-- Partial index for the templates list page, which unions a team's own
-- templates with every public template across all teams.
CREATE INDEX idx_templates_public ON templates(is_public) WHERE is_public;

-- Records the last time a team changed its slug. Used to enforce the 60-day
-- rename cooldown. NULL means the slug has never been changed (still the
-- auto-generated one).
ALTER TABLE teams ADD COLUMN slug_changed_at TIMESTAMPTZ;

-- Tombstone table: when a team changes its slug, the old slug is parked here
-- for 30 days so nobody else can immediately claim it and start serving
-- different templates under a name others may still reference. No redirect —
-- old "<slug>/<name>" references simply fail while the reservation is active
-- and after it expires (until someone re-registers the slug).
CREATE TABLE reserved_slugs (
    slug        TEXT PRIMARY KEY,
    team_id     UUID NOT NULL,
    reserved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_reserved_slugs_expires ON reserved_slugs(expires_at);

-- Rebrand the platform team's slug to 'wrenn'. Fresh installs already seed
-- 'wrenn'; this migrates databases seeded with the old 'platform' slug. Guarded
-- on the current value so a re-slugged team is never clobbered.
UPDATE teams SET slug = 'wrenn'
WHERE id = '00000000-0000-0000-0000-000000000000' AND slug = 'platform';

-- +goose Down
UPDATE teams SET slug = 'platform'
WHERE id = '00000000-0000-0000-0000-000000000000' AND slug = 'wrenn';
DROP TABLE reserved_slugs;
ALTER TABLE teams DROP COLUMN slug_changed_at;
DROP INDEX IF EXISTS idx_templates_public;
ALTER TABLE templates DROP COLUMN is_public;
