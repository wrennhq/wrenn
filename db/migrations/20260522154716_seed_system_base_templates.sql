-- +goose Up

-- Replace the old all-zeros "minimal" base template with the four system base
-- templates (ubuntu/alpine/arch/fedora). All are platform-owned (team_id
-- all-zeros) with reserved template IDs 0..3, default user wrenn-user.
--
-- Template IDs are well-known: the all-zeros UUID + low byte = {0,1,2,3}.
-- On disk each lives at images/teams/{base36(0)}/{base36(id)}/rootfs.ext4.

-- 0 → minimal-ubuntu (was "minimal").
UPDATE templates
SET name = 'minimal-ubuntu',
    default_user = 'wrenn-user'
WHERE id = '00000000-0000-0000-0000-000000000000';

-- Seed the row if it did not already exist (fresh DBs).
INSERT INTO templates (id, name, type, vcpus, memory_mb, size_bytes, team_id, default_user)
VALUES ('00000000-0000-0000-0000-000000000000', 'minimal-ubuntu', 'base', 1, 512, 0,
        '00000000-0000-0000-0000-000000000000', 'wrenn-user')
ON CONFLICT (id) DO NOTHING;

-- 1 → minimal-alpine, 2 → minimal-arch, 3 → minimal-fedora.
INSERT INTO templates (id, name, type, vcpus, memory_mb, size_bytes, team_id, default_user)
VALUES
    ('00000000-0000-0000-0000-000000000001', 'minimal-alpine', 'base', 1, 512, 0,
     '00000000-0000-0000-0000-000000000000', 'wrenn-user'),
    ('00000000-0000-0000-0000-000000000002', 'minimal-arch', 'base', 1, 512, 0,
     '00000000-0000-0000-0000-000000000000', 'wrenn-user'),
    ('00000000-0000-0000-0000-000000000003', 'minimal-fedora', 'base', 1, 512, 0,
     '00000000-0000-0000-0000-000000000000', 'wrenn-user')
ON CONFLICT (id) DO NOTHING;

-- Point the sandboxes.template column default at the new default base template.
ALTER TABLE sandboxes ALTER COLUMN template SET DEFAULT 'minimal-ubuntu';

-- +goose Down

ALTER TABLE sandboxes ALTER COLUMN template SET DEFAULT 'minimal';

DELETE FROM templates WHERE id IN (
    '00000000-0000-0000-0000-000000000001',
    '00000000-0000-0000-0000-000000000002',
    '00000000-0000-0000-0000-000000000003'
);

UPDATE templates
SET name = 'minimal',
    default_user = 'root'
WHERE id = '00000000-0000-0000-0000-000000000000';
