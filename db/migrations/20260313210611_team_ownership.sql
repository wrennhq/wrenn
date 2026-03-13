-- +goose Up

ALTER TABLE sandboxes
    ADD COLUMN team_id TEXT NOT NULL DEFAULT '';

UPDATE sandboxes SET team_id = owner_id WHERE owner_id != '';

ALTER TABLE sandboxes
    DROP COLUMN owner_id;

ALTER TABLE templates
    ADD COLUMN team_id TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_sandboxes_team ON sandboxes(team_id);
CREATE INDEX idx_templates_team ON templates(team_id);

-- +goose Down

ALTER TABLE sandboxes
    ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';

UPDATE sandboxes SET owner_id = team_id WHERE team_id != '';

ALTER TABLE sandboxes
    DROP COLUMN team_id;

ALTER TABLE templates
    DROP COLUMN team_id;

DROP INDEX IF EXISTS idx_sandboxes_team;
DROP INDEX IF EXISTS idx_templates_team;
