package service

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/validate"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

// Volume size bounds (MB).
const (
	MinVolumeSizeMB int32 = 100
	MaxVolumeSizeMB int32 = 1024 * 1024 // 1 TiB
)

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
}

// Create records a new detached volume. No host is chosen yet — a volume is
// pinned to a host only when first attached to a capsule.
func (s *VolumeService) Create(ctx context.Context, teamID pgtype.UUID, name string, sizeMB int32) (db.Volume, error) {
	if !teamID.Valid {
		return db.Volume{}, apperr.InvalidRequest.Msg("A team_id is required.")
	}
	if err := validate.SafeName(name); err != nil {
		return db.Volume{}, apperr.ValidationFailed.Msgf("Invalid volume name: %v.", err)
	}
	if sizeMB < MinVolumeSizeMB {
		return db.Volume{}, apperr.InvalidRequest.Msgf("Volume size must be at least %d MB.", MinVolumeSizeMB)
	}
	if sizeMB > MaxVolumeSizeMB {
		return db.Volume{}, apperr.InvalidRequest.Msgf("Volume size must not exceed %d MB.", MaxVolumeSizeMB)
	}

	vol, err := s.DB.InsertVolume(ctx, db.InsertVolumeParams{
		ID:     id.NewVolumeID(),
		TeamID: teamID,
		Name:   name,
		SizeMb: sizeMB,
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return db.Volume{}, apperr.VolumeNameTaken.New()
		}
		return db.Volume{}, fmt.Errorf("insert volume: %w", err)
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
