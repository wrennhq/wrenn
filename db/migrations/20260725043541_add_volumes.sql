-- +goose Up
-- External storage volumes: team-scoped block-storage disks that are attached
-- to a capsule at create time and mounted inside the guest. A volume is
-- host-local (Cloud Hypervisor can only attach a path on the same host), so it
-- is pinned to a host the first time it is attached and stays there. Volumes
-- are never auto-deleted: destroying a capsule frees its volumes back to
-- 'detached' (data preserved); removal is always an explicit delete.
CREATE TABLE volumes (
    id           UUID PRIMARY KEY,
    team_id      UUID NOT NULL REFERENCES teams(id),
    host_id      UUID REFERENCES hosts(id),        -- NULL until first attach; pins the volume thereafter
    name         TEXT NOT NULL,
    size_mb      INTEGER NOT NULL,
    status       TEXT NOT NULL DEFAULT 'detached',  -- detached | attaching | attached | deleting
    sandbox_id   UUID REFERENCES sandboxes(id),     -- set while attaching/attached, cleared on capsule destroy
    mount_path   TEXT NOT NULL DEFAULT '',          -- guest mount path in use; '' means the default /mnt/<vol-id>
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_attached_at TIMESTAMPTZ,
    last_updated TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (team_id, name)
);

CREATE INDEX idx_volumes_team ON volumes(team_id);
CREATE INDEX idx_volumes_host ON volumes(host_id);
CREATE INDEX idx_volumes_sandbox ON volumes(sandbox_id) WHERE sandbox_id IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS volumes;
