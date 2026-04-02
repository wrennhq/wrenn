-- +goose Up

-- 1. Add UUID id column to templates and make it the primary key.
ALTER TABLE templates ADD COLUMN id UUID DEFAULT gen_random_uuid();
UPDATE templates SET id = gen_random_uuid() WHERE id IS NULL;
ALTER TABLE templates ALTER COLUMN id SET NOT NULL;
ALTER TABLE templates DROP CONSTRAINT templates_pkey;
ALTER TABLE templates ADD PRIMARY KEY (id);

-- 2. Name becomes a display field with team-scoped uniqueness.
ALTER TABLE templates ADD CONSTRAINT uq_templates_team_name UNIQUE (team_id, name);

-- 3. Prevent team templates from using names that belong to global (platform) templates.
--    A team template insert/update with a name matching any platform template is rejected.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION check_global_template_name_collision()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.team_id != '00000000-0000-0000-0000-000000000000' THEN
        IF EXISTS (
            SELECT 1 FROM templates
            WHERE name = NEW.name
            AND team_id = '00000000-0000-0000-0000-000000000000'
        ) THEN
            RAISE EXCEPTION 'template name "%" is reserved by a global template', NEW.name
                USING ERRCODE = 'unique_violation';
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_check_global_template_name
    BEFORE INSERT OR UPDATE ON templates
    FOR EACH ROW
    EXECUTE FUNCTION check_global_template_name_collision();

-- 4. Seed the built-in "minimal" template so it appears in all listings.
--    Both id and team_id are the all-zeros UUID (platform sentinel).
INSERT INTO templates (id, name, type, vcpus, memory_mb, size_bytes, team_id)
VALUES (
    '00000000-0000-0000-0000-000000000000',
    'minimal',
    'base',
    1,
    512,
    0,
    '00000000-0000-0000-0000-000000000000'
) ON CONFLICT DO NOTHING;

-- 5. Add template UUID references to template_builds.
ALTER TABLE template_builds
    ADD COLUMN template_id UUID,
    ADD COLUMN team_id UUID;

-- 5. Add template UUID references to sandboxes.
ALTER TABLE sandboxes
    ADD COLUMN template_id UUID,
    ADD COLUMN template_team_id UUID;

-- +goose Down

ALTER TABLE sandboxes
    DROP COLUMN IF EXISTS template_team_id,
    DROP COLUMN IF EXISTS template_id;

ALTER TABLE template_builds
    DROP COLUMN IF EXISTS team_id,
    DROP COLUMN IF EXISTS template_id;

-- Remove the seeded minimal template.
DELETE FROM templates WHERE id = '00000000-0000-0000-0000-000000000000';

DROP TRIGGER IF EXISTS trg_check_global_template_name ON templates;
DROP FUNCTION IF EXISTS check_global_template_name_collision();

ALTER TABLE templates DROP CONSTRAINT IF EXISTS uq_templates_team_name;

ALTER TABLE templates DROP CONSTRAINT IF EXISTS templates_pkey;
ALTER TABLE templates ADD PRIMARY KEY (name);
ALTER TABLE templates DROP COLUMN IF EXISTS id;
