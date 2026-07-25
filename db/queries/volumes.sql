-- name: InsertVolume :one
INSERT INTO volumes (id, team_id, name, size_mb, status)
VALUES ($1, $2, $3, $4, 'detached')
RETURNING *;

-- name: GetVolume :one
SELECT * FROM volumes WHERE id = $1;

-- name: GetVolumeByTeam :one
SELECT * FROM volumes WHERE id = $1 AND team_id = $2;

-- name: GetVolumeByTeamAndName :one
-- Resolve a volume by its user-facing name. Names are unique per team, so this
-- is the name-based counterpart to GetVolumeByTeam and is what lets the API
-- accept "vl-cache" wherever it accepts "vol-<id>".
SELECT * FROM volumes WHERE team_id = $1 AND name = $2;

-- name: ListVolumesByTeam :many
SELECT * FROM volumes WHERE team_id = $1 ORDER BY created_at DESC;

-- name: ListVolumesBySandbox :many
SELECT * FROM volumes WHERE sandbox_id = $1 ORDER BY created_at DESC;

-- name: CountVolumesByHost :one
-- Guard used before a host can be removed: a host holding pinned volumes must
-- not be deleted out from under them.
SELECT COUNT(*) FROM volumes WHERE host_id = $1;

-- name: ReserveVolumeForAttach :one
-- Atomically claim a detached volume for a capsule about to boot. The CAS on
-- status = 'detached' enforces single-attach: a second capsule create racing
-- for the same volume finds no row and fails. sandbox_id is deliberately NOT
-- set here — the sandbox row does not exist yet, so setting the FK would fail.
-- It is set in MarkVolumeAttached once the capsule has booted.
UPDATE volumes
SET status       = 'attaching',
    last_updated = NOW()
WHERE id = $1 AND status = 'detached'
RETURNING *;

-- name: MarkVolumeAttached :one
-- Promote a reserved volume to attached once the capsule has booted with it.
-- Records the owning sandbox and pins the volume to the sandbox's host on
-- first-ever attach (COALESCE keeps an existing pin for a re-attached volume).
UPDATE volumes
SET status           = 'attached',
    host_id          = COALESCE(host_id, $2),
    sandbox_id       = $3,
    mount_path       = $4,
    last_attached_at = NOW(),
    last_updated     = NOW()
WHERE id = $1 AND status = 'attaching'
RETURNING *;

-- name: LinkVolumeReservation :exec
-- Stamp the owning sandbox onto a reservation as soon as the sandbox row
-- exists (ReserveVolumeForAttach runs before the insert, so it cannot set the
-- FK itself). This makes every in-flight reservation attributable to a capsule,
-- which is what lets the host monitor decide — against live host state —
-- whether a stuck 'attaching' row is safe to free.
UPDATE volumes
SET sandbox_id   = $2,
    last_updated = NOW()
WHERE id = $1 AND status = 'attaching';

-- name: ReleaseVolumeReservation :exec
-- Roll a reserved volume back to detached when the capsule create fails.
-- host_id is left untouched (a failed create never pins a fresh volume, since
-- MarkVolumeAttached is what sets the pin). Only call this once the host has
-- confirmed the capsule is not running — a volume freed while a VM still has
-- its backing file open can be re-attached elsewhere and corrupted.
UPDATE volumes
SET status       = 'detached',
    sandbox_id   = NULL,
    last_updated = NOW()
WHERE id = $1 AND status = 'attaching';

-- name: DetachVolumesBySandbox :exec
-- Terminal sweep run when a capsule reaches a terminal state (destroyed,
-- stopped, errored, or reaped): free every volume it held back to detached
-- (data and host pin preserved) so it can be reused or deleted. Keyed on
-- sandbox_id, which is stamped on at reservation time. Never deletes the
-- volume — removal is always explicit.
--
-- 'attaching' is swept alongside 'attached' so a reservation whose promotion
-- never landed (lost RPC response, DB blip) is freed here — on confirmed host
-- state — rather than on a blind timer.
UPDATE volumes
SET status       = 'detached',
    sandbox_id   = NULL,
    mount_path   = '',
    last_updated = NOW()
WHERE sandbox_id = $1 AND status IN ('attached', 'attaching');

-- name: DetachVolumesBySandboxIDs :exec
-- Bulk variant of DetachVolumesBySandbox for the host monitor, which stops
-- many sandboxes at once (e.g. after a host goes offline).
UPDATE volumes
SET status       = 'detached',
    sandbox_id   = NULL,
    mount_path   = '',
    last_updated = NOW()
WHERE sandbox_id = ANY($1::uuid[]) AND status IN ('attached', 'attaching');

-- name: DetachVolumesByHost :exec
-- Free and un-pin every volume on a host being force-deleted. The host (and its
-- backing files) are going away, so the volumes are reset to a fresh detached,
-- un-pinned state — the rows survive (never auto-deleted) and can be re-attached
-- on a new host. Also required before DeleteHost so the host_id FK does not
-- block deletion.
UPDATE volumes
SET status       = 'detached',
    sandbox_id   = NULL,
    host_id      = NULL,
    mount_path   = '',
    last_updated = NOW()
WHERE host_id = $1;

-- name: BeginVolumeDelete :one
-- Atomically claim a volume for deletion. The CAS blocks a concurrent attach
-- (which needs status='detached') so a volume can never be attached and deleted
-- at the same time. 'deleting' is also accepted so a delete that crashed
-- mid-flight can be retried rather than leaving the volume permanently stuck.
-- Returns the row (for host_id) when claimed; no row means it is not deletable
-- (attached/attaching, wrong team, or missing).
UPDATE volumes
SET status       = 'deleting',
    last_updated = NOW()
WHERE id = $1 AND team_id = $2 AND status IN ('detached', 'deleting')
RETURNING *;

-- name: AbortVolumeDelete :exec
-- Revert a delete claim back to detached when the host-side file removal fails,
-- so the volume stays usable rather than stuck in 'deleting'.
UPDATE volumes
SET status       = 'detached',
    last_updated = NOW()
WHERE id = $1 AND status = 'deleting';

-- name: ReleaseStaleVolumeReservations :execrows
-- Free volumes stuck in a transient state whose owning operation died before it
-- could ever reach a host — the control plane crashed between reserving a
-- volume and inserting the sandbox row, or mid-delete. Run periodically by the
-- volume reaper.
--
-- Deliberately limited to unattributed rows (sandbox_id IS NULL). A reservation
-- that already carries a sandbox_id may correspond to a capsule that really did
-- boot with the volume mounted — and a timer cannot tell the difference. Those
-- are freed only by the host-monitor sweep, which first confirms against the
-- host that the capsule is gone. Freeing a live volume here would let a second
-- capsule attach the same backing file and corrupt it.
--
-- The cutoff ($1 = now - grace) MUST exceed the capsule create timeout so a
-- legitimately in-flight attach is never freed out from under a booting capsule.
UPDATE volumes
SET status       = 'detached',
    mount_path   = '',
    last_updated = NOW()
WHERE status IN ('attaching', 'deleting')
  AND sandbox_id IS NULL
  AND last_updated < $1;

-- name: DeleteVolumeRow :exec
-- Drop the row only while the delete claim still holds. Without the status
-- guard a delete that lost its claim mid-flight (reaped, or aborted and then
-- re-attached by a racing capsule create) would erase the record of a volume
-- that is once again in use.
DELETE FROM volumes WHERE id = $1 AND team_id = $2 AND status = 'deleting';
