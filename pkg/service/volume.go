package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/units"
	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/validate"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

// MinVolumeSizeMB is the smallest volume that can be created. Below roughly
// this size an ext4 filesystem's own metadata dominates the usable space.
const MinVolumeSizeMB int32 = 100

// DefaultMaxVolumeSizeMB is the per-volume ceiling used when a VolumeService is
// constructed without one. Deployments override it with WRENN_MAX_VOLUME_SIZE.
const DefaultMaxVolumeSizeMB int32 = 20 * 1024 // 20 GiB

// VolumeService provides external-storage-volume lifecycle operations. Volume
// metadata lives entirely in Postgres; the only host interaction is deleting a
// pinned volume's backing file. Unlike SandboxService there is no async state
// machine — create/list/get are pure DB ops and delete is synchronous.
//
// Attach/detach are not offered here: a volume is attached only at capsule
// create (see SandboxService.Create) and freed when the capsule is destroyed.
type VolumeService struct {
	DB   *db.Queries
	Pool *lifecycle.HostClientPool

	// MaxSizeMB caps the size of a single volume. Zero means
	// DefaultMaxVolumeSizeMB.
	MaxSizeMB int32
}

func (s *VolumeService) maxSizeMB() int32 {
	if s.MaxSizeMB > 0 {
		return s.MaxSizeMB
	}
	return DefaultMaxVolumeSizeMB
}

// Create records a new detached volume. No host is chosen yet — a volume is
// pinned to a host only when first attached to a capsule.
//
// name is optional: an empty name yields "vl-<volume id>", so a caller that
// does not care about naming still gets a stable, addressable handle. A
// supplied name is normalized to its "vl-"-prefixed slug form.
func (s *VolumeService) Create(ctx context.Context, teamID pgtype.UUID, name string, sizeMB int32) (db.Volume, error) {
	if !teamID.Valid {
		return db.Volume{}, apperr.InvalidRequest.Msg("A team_id is required.")
	}
	if sizeMB < MinVolumeSizeMB {
		return db.Volume{}, apperr.InvalidRequest.Msgf("Volume size must be at least %s.", units.FormatMB(int(MinVolumeSizeMB)))
	}
	if max := s.maxSizeMB(); sizeMB > max {
		return db.Volume{}, apperr.InvalidRequest.Msgf("Volume size must not exceed %s.", units.FormatMB(int(max)))
	}

	volumeID := id.NewVolumeID()
	// A nameless volume is named after itself. id.FormatVolumeID carries the
	// "vol-" ID prefix, so use the bare base36 body to keep the name in the
	// "vl-" namespace.
	if strings.TrimSpace(name) == "" {
		name = validate.VolumeNamePrefix + id.UUIDToBase36(volumeID.Bytes)
	}
	name, err := validate.VolumeName(name)
	if err != nil {
		return db.Volume{}, apperr.ValidationFailed.Msgf("Invalid volume name: %v.", err)
	}

	vol, err := s.DB.InsertVolume(ctx, db.InsertVolumeParams{
		ID:     volumeID,
		TeamID: teamID,
		Name:   name,
		SizeMb: sizeMB,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return db.Volume{}, apperr.VolumeNameTaken.Msgf("A volume named %q already exists in this team.", name)
		}
		return db.Volume{}, fmt.Errorf("insert volume: %w", err)
	}
	return vol, nil
}

// Resolve turns a user-supplied reference into the team's volume. A reference
// is either a volume ID ("vol-<base36>") or a name ("vl-cache", or plain
// "cache" — the prefix is optional on input). The two namespaces cannot
// collide: an ID always carries the "vol-" prefix, a name always "vl-".
func (s *VolumeService) Resolve(ctx context.Context, teamID pgtype.UUID, ref string) (db.Volume, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return db.Volume{}, apperr.InvalidRequest.Msg("A volume ID or name is required.")
	}

	if strings.HasPrefix(ref, id.PrefixVolume) {
		volumeID, err := id.ParseVolumeID(ref)
		if err != nil {
			return db.Volume{}, apperr.InvalidRequest.WrapMsg(err, "Invalid volume ID.")
		}
		return s.Get(ctx, volumeID, teamID)
	}

	name, err := validate.VolumeName(ref)
	if err != nil {
		return db.Volume{}, apperr.InvalidRequest.Msgf("Invalid volume reference: %v.", err)
	}
	vol, err := s.DB.GetVolumeByTeamAndName(ctx, db.GetVolumeByTeamAndNameParams{TeamID: teamID, Name: name})
	if err != nil {
		return db.Volume{}, apperr.VolumeNotFound.WrapMsg(err, fmt.Sprintf("Volume %q not found.", name))
	}
	return vol, nil
}

// List returns all volumes owned by the team, newest first.
func (s *VolumeService) List(ctx context.Context, teamID pgtype.UUID) ([]db.Volume, error) {
	return s.DB.ListVolumesByTeam(ctx, teamID)
}

// Get returns a single volume owned by the team.
func (s *VolumeService) Get(ctx context.Context, volumeID, teamID pgtype.UUID) (db.Volume, error) {
	vol, err := s.DB.GetVolumeByTeam(ctx, db.GetVolumeByTeamParams{ID: volumeID, TeamID: teamID})
	if err != nil {
		return db.Volume{}, apperr.VolumeNotFound.Wrap(err)
	}
	return vol, nil
}

// Delete removes a detached volume: it deletes the backing file on the volume's
// pinned host (if any), then the DB row. An attached volume cannot be deleted —
// the capsule using it must be destroyed first.
func (s *VolumeService) Delete(ctx context.Context, volumeID, teamID pgtype.UUID) error {
	// Atomically claim the volume for deletion (detached → deleting). The CAS
	// blocks a concurrent attach — which needs status='detached' — closing the
	// TOCTOU window between checking the status and removing the file/row.
	vol, err := s.DB.BeginVolumeDelete(ctx, db.BeginVolumeDeleteParams{ID: volumeID, TeamID: teamID})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("claim volume for delete: %w", err)
		}
		// Not claimable: either the volume is missing / not this team's, or it
		// is currently attached/attaching. Distinguish for a precise error.
		if _, gerr := s.DB.GetVolumeByTeam(ctx, db.GetVolumeByTeamParams{ID: volumeID, TeamID: teamID}); gerr != nil {
			return apperr.VolumeNotFound.Wrap(gerr)
		}
		return apperr.VolumeInUse.New()
	}

	// Remove the backing file from the host that holds it. A volume that was
	// never attached (host_id NULL) has no file on any host — just drop the row.
	if vol.HostID.Valid {
		host, err := s.DB.GetHost(ctx, vol.HostID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			// The pinned host was deleted; its disk (and the volume's file) are
			// gone with it. Nothing to remove — proceed to drop the row so the
			// volume isn't left permanently un-deletable.
		case err != nil:
			_ = s.DB.AbortVolumeDelete(ctx, volumeID) // revert to detached
			return apperr.HostNotFound.Wrap(err)
		default:
			agent, err := s.Pool.GetForHost(host)
			if err != nil {
				_ = s.DB.AbortVolumeDelete(ctx, volumeID)
				return fmt.Errorf("get agent client: %w", err)
			}
			// NotFound is fine — the file may already be gone. Any other error
			// must fail the delete (reverting the claim) so we never drop the
			// row while the file lingers on a reachable host.
			if _, err := agent.DeleteVolume(ctx, connect.NewRequest(&pb.DeleteVolumeRequest{
				TeamId:   id.UUIDString(teamID),
				VolumeId: id.UUIDString(volumeID),
			})); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
				_ = s.DB.AbortVolumeDelete(ctx, volumeID)
				return fmt.Errorf("delete volume file on host: %w", err)
			}
		}
	}

	if err := s.DB.DeleteVolumeRow(ctx, db.DeleteVolumeRowParams{ID: volumeID, TeamID: teamID}); err != nil {
		return fmt.Errorf("delete volume row: %w", err)
	}
	return nil
}
