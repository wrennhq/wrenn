-- +goose Up
-- Volume names are now always "vl-"-prefixed slugs: lowercase alphanumerics in
-- dash-separated groups, at most 40 characters. The prefix makes a name
-- self-describing and — since volume IDs use the distinct "vol-" prefix — lets
-- one API path segment be resolved as either an ID or a name unambiguously.
--
-- Backfill the names written before the rule existed. They were validated with
-- the looser SafeName allowlist, so coerce them into slug shape first
-- (lowercase, non-slug characters to dashes, collapse and trim dashes) and only
-- then prefix. A name that survives as empty falls back to the volume's ID,
-- which is also the default a nameless volume now gets.
UPDATE volumes
SET name = LEFT(
        'vl-' || COALESCE(
            NULLIF(TRIM(BOTH '-' FROM REGEXP_REPLACE(LOWER(name), '[^a-z0-9]+', '-', 'g')), ''),
            REPLACE(id::text, '-', '')
        ),
        40
    )
WHERE name !~ '^vl-[a-z0-9]+(-[a-z0-9]+)*$';

-- The truncation and coercion above can collide two previously-distinct names
-- within a team, which UNIQUE (team_id, name) would have rejected. Nothing here
-- can fail silently, so re-assert the invariant: any row still not matching the
-- rule aborts the migration rather than leaving invalid names behind.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM volumes WHERE name !~ '^vl-[a-z0-9]+(-[a-z0-9]+)*$') THEN
        RAISE EXCEPTION 'volume name backfill left rows that are not valid vl- slugs';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- Strip the prefix back off. The original pre-coercion spelling is not
-- recoverable, so this restores the shape, not the exact former value.
UPDATE volumes
SET name = REGEXP_REPLACE(name, '^vl-', '')
WHERE name ~ '^vl-';
