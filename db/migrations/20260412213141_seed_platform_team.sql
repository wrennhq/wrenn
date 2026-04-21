-- +goose Up

-- Seed the platform team row. This is the sentinel team (all-zeros UUID) that
-- owns platform-wide resources: global templates, admin-created capsules, etc.
-- No user can become a member of this team — it exists solely to satisfy
-- foreign key constraints and to act as a namespace for platform resources.
INSERT INTO teams (id, name, slug)
VALUES ('00000000-0000-0000-0000-000000000000', 'Platform', 'platform')
ON CONFLICT (id) DO NOTHING;

-- +goose Down
-- Delete dependent rows that reference the platform team via foreign keys.
-- Order matters: children before parent.
DELETE FROM sandboxes WHERE team_id = '00000000-0000-0000-0000-000000000000';
DELETE FROM team_api_keys WHERE team_id = '00000000-0000-0000-0000-000000000000';
DELETE FROM users_teams WHERE team_id = '00000000-0000-0000-0000-000000000000';
DELETE FROM hosts WHERE team_id = '00000000-0000-0000-0000-000000000000';
DELETE FROM teams WHERE id = '00000000-0000-0000-0000-000000000000';
