-- +goose Up
-- +goose StatementBegin
-- Session IDs are now stored as sha256(raw_sid) hex so a DB/Redis dump
-- cannot be replayed as session cookies. Existing sessions hold raw SIDs
-- in id; they are unrecoverable under the new scheme and must be wiped.
-- Users will need to log in again after this migration.
TRUNCATE TABLE sessions;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Down: nothing to do schematically. Hashed rows remain but will never
-- match a raw cookie under the old code path; safest is to wipe again.
TRUNCATE TABLE sessions;
-- +goose StatementEnd
