-- +goose Up

-- users_teams: remove membership when user is deleted
ALTER TABLE users_teams DROP CONSTRAINT users_teams_user_id_fkey;
ALTER TABLE users_teams ADD CONSTRAINT users_teams_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

-- oauth_providers: remove auth links when user is deleted
ALTER TABLE oauth_providers DROP CONSTRAINT oauth_providers_user_id_fkey;
ALTER TABLE oauth_providers ADD CONSTRAINT oauth_providers_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

-- admin_permissions: remove permissions when user is deleted
ALTER TABLE admin_permissions DROP CONSTRAINT admin_permissions_user_id_fkey;
ALTER TABLE admin_permissions ADD CONSTRAINT admin_permissions_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

-- team_api_keys.created_by: make nullable, SET NULL on user delete
ALTER TABLE team_api_keys ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE team_api_keys DROP CONSTRAINT team_api_keys_created_by_fkey;
ALTER TABLE team_api_keys ADD CONSTRAINT team_api_keys_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL;

-- hosts.created_by: make nullable, SET NULL on user delete
ALTER TABLE hosts ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE hosts DROP CONSTRAINT hosts_created_by_fkey;
ALTER TABLE hosts ADD CONSTRAINT hosts_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL;

-- host_tokens.created_by: make nullable, SET NULL on user delete
ALTER TABLE host_tokens ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE host_tokens DROP CONSTRAINT host_tokens_created_by_fkey;
ALTER TABLE host_tokens ADD CONSTRAINT host_tokens_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL;

-- +goose Down

-- Revert host_tokens.created_by
ALTER TABLE host_tokens DROP CONSTRAINT host_tokens_created_by_fkey;
UPDATE host_tokens SET created_by = '00000000-0000-0000-0000-000000000000' WHERE created_by IS NULL;
ALTER TABLE host_tokens ALTER COLUMN created_by SET NOT NULL;
ALTER TABLE host_tokens ADD CONSTRAINT host_tokens_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES users(id);

-- Revert hosts.created_by
ALTER TABLE hosts DROP CONSTRAINT hosts_created_by_fkey;
UPDATE hosts SET created_by = '00000000-0000-0000-0000-000000000000' WHERE created_by IS NULL;
ALTER TABLE hosts ALTER COLUMN created_by SET NOT NULL;
ALTER TABLE hosts ADD CONSTRAINT hosts_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES users(id);

-- Revert team_api_keys.created_by
ALTER TABLE team_api_keys DROP CONSTRAINT team_api_keys_created_by_fkey;
UPDATE team_api_keys SET created_by = '00000000-0000-0000-0000-000000000000' WHERE created_by IS NULL;
ALTER TABLE team_api_keys ALTER COLUMN created_by SET NOT NULL;
ALTER TABLE team_api_keys ADD CONSTRAINT team_api_keys_created_by_fkey
    FOREIGN KEY (created_by) REFERENCES users(id);

-- Revert admin_permissions
ALTER TABLE admin_permissions DROP CONSTRAINT admin_permissions_user_id_fkey;
ALTER TABLE admin_permissions ADD CONSTRAINT admin_permissions_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id);

-- Revert oauth_providers
ALTER TABLE oauth_providers DROP CONSTRAINT oauth_providers_user_id_fkey;
ALTER TABLE oauth_providers ADD CONSTRAINT oauth_providers_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id);

-- Revert users_teams
ALTER TABLE users_teams DROP CONSTRAINT users_teams_user_id_fkey;
ALTER TABLE users_teams ADD CONSTRAINT users_teams_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users(id);
