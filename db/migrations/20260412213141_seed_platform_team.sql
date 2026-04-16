-- +goose Up

-- Seed the platform team row. This is the sentinel team (all-zeros UUID) that
-- owns platform-wide resources: global templates, admin-created capsules, etc.
-- No user can become a member of this team — it exists solely to satisfy
-- foreign key constraints and to act as a namespace for platform resources.
INSERT INTO teams (id, name, slug)
VALUES ('00000000-0000-0000-0000-000000000000', 'Platform', 'platform')
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DELETE FROM teams WHERE id = '00000000-0000-0000-0000-000000000000';
