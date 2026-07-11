-- +goose Up

-- Bump the default sandbox specs from 1 vCPU / 512 MB to 2 vCPUs / 2048 MB.
-- Applies to any row inserted without explicit vcpus/memory_mb across the
-- sandboxes, templates, and template_builds tables.

ALTER TABLE sandboxes ALTER COLUMN vcpus SET DEFAULT 2;
ALTER TABLE sandboxes ALTER COLUMN memory_mb SET DEFAULT 2048;

ALTER TABLE templates ALTER COLUMN vcpus SET DEFAULT 2;
ALTER TABLE templates ALTER COLUMN memory_mb SET DEFAULT 2048;

ALTER TABLE template_builds ALTER COLUMN vcpus SET DEFAULT 2;
ALTER TABLE template_builds ALTER COLUMN memory_mb SET DEFAULT 2048;

-- +goose Down

ALTER TABLE sandboxes ALTER COLUMN vcpus SET DEFAULT 1;
ALTER TABLE sandboxes ALTER COLUMN memory_mb SET DEFAULT 512;

ALTER TABLE templates ALTER COLUMN vcpus SET DEFAULT 1;
ALTER TABLE templates ALTER COLUMN memory_mb SET DEFAULT 512;

ALTER TABLE template_builds ALTER COLUMN vcpus SET DEFAULT 1;
ALTER TABLE template_builds ALTER COLUMN memory_mb SET DEFAULT 512;
