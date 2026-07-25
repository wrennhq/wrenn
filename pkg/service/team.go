package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/auth/session"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/validate"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

// slugChangeCooldown is the minimum time between team slug changes.
const slugChangeCooldown = 60 * 24 * time.Hour

var teamNameRE = regexp.MustCompile(`^[A-Za-z0-9 _\-@']{1,128}$`)

// TeamService provides team management operations.
type TeamService struct {
	DB       *db.Queries
	Pool     *pgxpool.Pool
	HostPool *lifecycle.HostClientPool
	// Sessions drops cached sessions when a member is removed so their access
	// is stripped at the next request. Optional: nil in contexts without a
	// session store (revocation is then skipped).
	Sessions *session.Service
}

// TeamWithRole pairs a team with the calling user's role in it.
type TeamWithRole struct {
	db.Team
	Role string `json:"role"`
}

// MemberInfo is a team member with resolved user details.
type MemberInfo struct {
	UserID   string    `json:"user_id"`
	Name     string    `json:"name"`
	Email    string    `json:"email"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joined_at"`
}

// callerRole fetches the calling user's role in the given team from DB.
// Returns an error wrapping "forbidden" if the caller is not a member.
func (s *TeamService) callerRole(ctx context.Context, teamID, callerUserID pgtype.UUID) (string, error) {
	m, err := s.DB.GetTeamMembership(ctx, db.GetTeamMembershipParams{
		UserID: callerUserID,
		TeamID: teamID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", apperr.Forbidden.Msg("You are not a member of this team.")
		}
		return "", fmt.Errorf("get membership: %w", err)
	}
	return m.Role, nil
}

// requireAdmin returns an error if the caller is not an admin or owner.
func requireAdmin(role string) error {
	if role != "owner" && role != "admin" {
		return apperr.Forbidden.Msg("Admin or owner role required.")
	}
	return nil
}

// GetTeam returns the team by ID. Returns an error if the team is deleted or not found.
func (s *TeamService) GetTeam(ctx context.Context, teamID pgtype.UUID) (db.Team, error) {
	team, err := s.DB.GetTeam(ctx, teamID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return db.Team{}, apperr.TeamNotFound.New()
		}
		return db.Team{}, fmt.Errorf("get team: %w", err)
	}
	if team.DeletedAt.Valid {
		return db.Team{}, apperr.TeamNotFound.New()
	}
	return team, nil
}

// ListTeamsForUser returns all active teams the user belongs to, with their role in each.
func (s *TeamService) ListTeamsForUser(ctx context.Context, userID pgtype.UUID) ([]TeamWithRole, error) {
	rows, err := s.DB.GetTeamsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	result := make([]TeamWithRole, len(rows))
	for i, r := range rows {
		result[i] = TeamWithRole{
			Team: db.Team{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, IsByoc: r.IsByoc, Slug: r.Slug, DeletedAt: r.DeletedAt},
			Role: r.Role,
		}
	}
	return result, nil
}

// CreateTeam creates a new team owned by the given user.
func (s *TeamService) CreateTeam(ctx context.Context, ownerUserID pgtype.UUID, name string) (TeamWithRole, error) {
	if !teamNameRE.MatchString(name) {
		return TeamWithRole{}, apperr.ValidationFailed.Msg("Team name must be 1–128 characters: letters, numbers, spaces, and underscores.").With("field", "name")
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return TeamWithRole{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	qtx := s.DB.WithTx(tx)

	teamID := id.NewTeamID()
	team, err := qtx.InsertTeam(ctx, db.InsertTeamParams{
		ID:   teamID,
		Name: name,
		Slug: id.NewTeamSlug(),
	})
	if err != nil {
		return TeamWithRole{}, fmt.Errorf("insert team: %w", err)
	}

	if err := qtx.InsertTeamMember(ctx, db.InsertTeamMemberParams{
		UserID:    ownerUserID,
		TeamID:    teamID,
		IsDefault: false,
		Role:      "owner",
	}); err != nil {
		return TeamWithRole{}, fmt.Errorf("insert owner: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return TeamWithRole{}, fmt.Errorf("commit: %w", err)
	}

	return TeamWithRole{Team: team, Role: "owner"}, nil
}

// RenameTeam updates the team name. Caller must be admin or owner (verified from DB).
func (s *TeamService) RenameTeam(ctx context.Context, teamID, callerUserID pgtype.UUID, newName string) error {
	if !teamNameRE.MatchString(newName) {
		return apperr.ValidationFailed.Msg("Team name must be 1–128 characters: letters, numbers, spaces, and underscores.").With("field", "name")
	}

	role, err := s.callerRole(ctx, teamID, callerUserID)
	if err != nil {
		return err
	}
	if err := requireAdmin(role); err != nil {
		return err
	}

	if err := s.DB.UpdateTeamName(ctx, db.UpdateTeamNameParams{ID: teamID, Name: newName}); err != nil {
		return fmt.Errorf("update name: %w", err)
	}
	return nil
}

// SlugChangeAllowedAt returns the earliest time the team may change its slug
// again and whether a cooldown is currently active. If the slug has never been
// changed, no cooldown applies.
func SlugChangeAllowedAt(t db.Team) (time.Time, bool) {
	if !t.SlugChangedAt.Valid {
		return time.Time{}, false
	}
	allowedAt := t.SlugChangedAt.Time.Add(slugChangeCooldown)
	if time.Now().Before(allowedAt) {
		return allowedAt, true
	}
	return time.Time{}, false
}

// SlugCheck is the result of a slug availability probe.
type SlugCheck struct {
	Available bool `json:"available"`
}

// CheckSlug reports whether the given slug can be adopted right now. It applies
// the format, reserved-word, uniqueness, and tombstone rules so the UI can
// validate before the user commits. It does NOT apply the 60-day cooldown —
// that gates the whole editor and is surfaced separately via SlugChangeAllowedAt.
//
// The result is fully team independent: a slug is available unless a live team
// holds it or it sits in the 30-day tombstone. There is no self-reclaim carve-out
// because the 30-day reservation always expires before the 60-day cooldown lets
// the same team change again, so a team can never encounter its own reservation.
func (s *TeamService) CheckSlug(ctx context.Context, slug string) (SlugCheck, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if err := validate.TeamSlug(slug); err != nil {
		return SlugCheck{Available: false}, nil
	}

	if _, err := s.DB.GetTeamBySlug(ctx, slug); err == nil {
		return SlugCheck{Available: false}, nil
	} else if err != pgx.ErrNoRows {
		return SlugCheck{}, fmt.Errorf("check slug availability: %w", err)
	}

	if _, err := s.DB.GetActiveReservedSlug(ctx, slug); err == nil {
		return SlugCheck{Available: false}, nil
	} else if err != pgx.ErrNoRows {
		return SlugCheck{}, fmt.Errorf("check reserved slug: %w", err)
	}

	return SlugCheck{Available: true}, nil
}

// ChangeSlug updates the team's URL slug. Caller must be admin or owner
// (verified from DB). A team may change its slug at most once every 60 days.
// The previous slug is parked in the reserved_slugs tombstone for 30 days so
// another team cannot immediately reclaim it and serve different templates
// under a name others may still reference. Returns the previous slug on success
// so callers can record it in the audit log.
func (s *TeamService) ChangeSlug(ctx context.Context, teamID, callerUserID pgtype.UUID, newSlug string) (oldSlug string, err error) {
	newSlug = strings.ToLower(strings.TrimSpace(newSlug))
	if verr := validate.TeamSlug(newSlug); verr != nil {
		return "", apperr.ValidationFailed.WrapMsg(verr, verr.Error()).With("field", "slug")
	}

	role, err := s.callerRole(ctx, teamID, callerUserID)
	if err != nil {
		return "", err
	}
	if err := requireAdmin(role); err != nil {
		return "", err
	}

	team, err := s.DB.GetTeam(ctx, teamID)
	if err != nil {
		return "", apperr.TeamNotFound.Wrap(err)
	}
	if team.DeletedAt.Valid {
		return "", apperr.TeamNotFound.Msg("Team not found.")
	}
	if team.Slug == newSlug {
		return team.Slug, nil // no-op, don't burn the cooldown
	}

	// Enforce the 60-day cooldown between changes.
	if team.SlugChangedAt.Valid {
		nextAllowed := team.SlugChangedAt.Time.Add(slugChangeCooldown)
		if time.Now().Before(nextAllowed) {
			return "", apperr.Conflict.Msgf("Team slug can only be changed once every 60 days. Next change allowed after %s.", nextAllowed.Format("2006-01-02"))
		}
	}

	// The new slug must not belong to a live team.
	if _, err := s.DB.GetTeamBySlug(ctx, newSlug); err == nil {
		return "", apperr.Conflict.Msg(newSlug+" unavailable").With("field", "slug")
	} else if err != pgx.ErrNoRows {
		return "", fmt.Errorf("check slug availability: %w", err)
	}

	// The new slug must not be tombstoned by *another* team. A team may reclaim
	// a slug it parked itself.
	if rsv, err := s.DB.GetActiveReservedSlug(ctx, newSlug); err == nil {
		if rsv.TeamID != teamID {
			return "", apperr.Conflict.Msg(newSlug+" unavailable").With("field", "slug")
		}
	} else if err != pgx.ErrNoRows {
		return "", fmt.Errorf("check reserved slug: %w", err)
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	qtx := s.DB.WithTx(tx)

	// Park the outgoing slug for 30 days.
	if err := qtx.InsertReservedSlug(ctx, db.InsertReservedSlugParams{Slug: team.Slug, TeamID: teamID}); err != nil {
		return "", fmt.Errorf("reserve old slug: %w", err)
	}
	// Clear any tombstone this team held on the incoming slug (self-reclaim).
	if err := qtx.DeleteReservedSlug(ctx, newSlug); err != nil {
		return "", fmt.Errorf("clear reserved slug: %w", err)
	}
	if err := qtx.UpdateTeamSlug(ctx, db.UpdateTeamSlugParams{ID: teamID, Slug: newSlug}); err != nil {
		return "", fmt.Errorf("update slug: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return team.Slug, nil
}

// DeleteTeam soft-deletes the team and destroys all running/paused/starting sandboxes.
// Caller must be owner (verified from DB). All DB records (sandboxes, keys, templates)
// are preserved; only the team's deleted_at is set and active VMs are stopped.
func (s *TeamService) DeleteTeam(ctx context.Context, teamID, callerUserID pgtype.UUID) error {
	role, err := s.callerRole(ctx, teamID, callerUserID)
	if err != nil {
		return err
	}
	if role != "owner" {
		return apperr.Forbidden.Msg("Only the owner can delete a team.")
	}

	return s.deleteTeamCore(ctx, teamID)
}

// deleteTeamCore contains the shared team deletion logic:
// destroy active sandboxes, clean up templates, soft-delete the team.
func (s *TeamService) deleteTeamCore(ctx context.Context, teamID pgtype.UUID) error {
	// Collect active sandboxes and stop them.
	sandboxes, err := s.DB.ListActiveSandboxesByTeam(ctx, teamID)
	if err != nil {
		return fmt.Errorf("list active sandboxes: %w", err)
	}

	var stopIDs []pgtype.UUID
	for _, sb := range sandboxes {
		host, hostErr := s.DB.GetHost(ctx, sb.HostID)
		if hostErr == nil {
			agent, agentErr := s.HostPool.GetForHost(host)
			if agentErr == nil {
				if _, err := agent.DestroySandbox(ctx, connect.NewRequest(&pb.DestroySandboxRequest{
					SandboxId: id.FormatSandboxID(sb.ID),
				})); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
					slog.Warn("team delete: failed to destroy sandbox", "sandbox_id", id.FormatSandboxID(sb.ID), "error", err)
				}
			}
		}
		stopIDs = append(stopIDs, sb.ID)
	}

	if len(stopIDs) > 0 {
		if err := s.DB.BulkUpdateStatusByIDs(ctx, db.BulkUpdateStatusByIDsParams{
			Column1: stopIDs,
			Status:  "stopped",
		}); err != nil {
			// Do not proceed to soft-delete if sandbox statuses couldn't be updated,
			// as that would leave orphaned "running" records for a deleted team.
			return fmt.Errorf("update sandbox statuses: %w", err)
		}

		// Free any volumes those capsules held. The destroys above already ran
		// against the host, so this is the same confirmed-gone signal the host
		// monitor uses — no VM is left holding a backing file open. Doing it here
		// (rather than relying on the volume cleanup below) keeps volume state
		// consistent even if that cleanup fails.
		if err := s.DB.DetachVolumesBySandboxIDs(ctx, stopIDs); err != nil {
			slog.Warn("team delete: failed to detach volumes", "team_id", id.FormatTeamID(teamID), "error", err)
		}
	}

	// Delete sandbox metrics for this team.
	if err := s.DB.DeleteMetricPointsByTeam(ctx, teamID); err != nil {
		slog.Warn("team delete: failed to delete metric points", "team_id", id.FormatTeamID(teamID), "error", err)
	}
	if err := s.DB.DeleteMetricsSnapshotsByTeam(ctx, teamID); err != nil {
		slog.Warn("team delete: failed to delete metrics snapshots", "team_id", id.FormatTeamID(teamID), "error", err)
	}

	// Delete all API keys for this team.
	if err := s.DB.DeleteAPIKeysByTeam(ctx, teamID); err != nil {
		slog.Warn("team delete: failed to delete API keys", "team_id", id.FormatTeamID(teamID), "error", err)
	}

	// Delete all channels for this team.
	if err := s.DB.DeleteAllChannelsByTeam(ctx, teamID); err != nil {
		slog.Warn("team delete: failed to delete channels", "team_id", id.FormatTeamID(teamID), "error", err)
	}

	// Clean up team-owned templates and storage volumes from all hosts in the
	// background.
	go s.cleanupTeamTemplates(context.Background(), teamID)
	go s.cleanupTeamVolumes(context.Background(), teamID)

	if err := s.DB.SoftDeleteTeam(ctx, teamID); err != nil {
		return fmt.Errorf("soft delete team: %w", err)
	}
	return nil
}

// cleanupTeamTemplates deletes all template files for a team from all online hosts,
// then removes the DB records. Called asynchronously during team deletion.
func (s *TeamService) cleanupTeamTemplates(ctx context.Context, teamID pgtype.UUID) {
	templates, err := s.DB.ListTemplatesByTeamOnly(ctx, teamID)
	if err != nil {
		slog.Warn("team delete: failed to list templates for cleanup", "team_id", id.FormatTeamID(teamID), "error", err)
		return
	}
	if len(templates) == 0 {
		return
	}

	hosts, err := s.DB.ListActiveHosts(ctx)
	if err != nil {
		slog.Warn("team delete: failed to list hosts for template cleanup", "error", err)
		return
	}

	for _, tmpl := range templates {
		for _, host := range hosts {
			if host.Status != "online" {
				continue
			}
			agent, err := s.HostPool.GetForHost(host)
			if err != nil {
				continue
			}
			if _, err := agent.DeleteSnapshot(ctx, connect.NewRequest(&pb.DeleteSnapshotRequest{
				TeamId:     id.UUIDString(tmpl.TeamID),
				TemplateId: id.UUIDString(tmpl.ID),
			})); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
				slog.Warn("team delete: failed to delete template on host",
					"host_id", id.FormatHostID(host.ID),
					"template", tmpl.Name,
					"error", err,
				)
			}
		}
	}

	// Remove DB records.
	if err := s.DB.DeleteTemplatesByTeam(ctx, teamID); err != nil {
		slog.Warn("team delete: failed to delete template records", "team_id", id.FormatTeamID(teamID), "error", err)
	}
}

// cleanupTeamVolumes deletes every storage volume owned by a team being deleted:
// the backing file on each volume's pinned host, then the DB records. Called
// asynchronously during team deletion.
//
// Normal volume deletion is always explicit (VolumeService.Delete) — a destroyed
// capsule only frees its volumes, it never removes them. Team deletion is the
// exception: the owner of the volumes is going away, so leaving the data behind
// would orphan it on the host with no way left to reach it.
//
// Unlike VolumeService.Delete there is no per-volume status claim: the team's
// capsules were destroyed before this ran, so nothing can be attaching or
// re-attaching. Records are dropped even when a host is unreachable, matching
// template cleanup — a row kept for a deleted team is unreachable forever and
// would keep blocking deletion of the host it is pinned to.
func (s *TeamService) cleanupTeamVolumes(ctx context.Context, teamID pgtype.UUID) {
	volumes, err := s.DB.ListVolumesByTeam(ctx, teamID)
	if err != nil {
		slog.Warn("team delete: failed to list volumes for cleanup", "team_id", id.FormatTeamID(teamID), "error", err)
		return
	}
	if len(volumes) == 0 {
		return
	}

	for _, vol := range volumes {
		// A volume that was never attached has no file on any host, and one whose
		// pinned host is gone lost its data with that host's disk. Either way
		// there is nothing to remove — just drop the record below.
		if !vol.HostID.Valid {
			continue
		}
		host, err := s.DB.GetHost(ctx, vol.HostID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("team delete: failed to load volume host",
					"volume_id", id.FormatVolumeID(vol.ID), "error", err)
			}
			continue
		}
		agent, err := s.HostPool.GetForHost(host)
		if err != nil {
			slog.Warn("team delete: failed to reach volume host",
				"host_id", id.FormatHostID(host.ID), "volume_id", id.FormatVolumeID(vol.ID), "error", err)
			continue
		}
		if _, err := agent.DeleteVolume(ctx, connect.NewRequest(&pb.DeleteVolumeRequest{
			TeamId:   id.UUIDString(teamID),
			VolumeId: id.UUIDString(vol.ID),
		})); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
			slog.Warn("team delete: failed to delete volume on host",
				"host_id", id.FormatHostID(host.ID), "volume_id", id.FormatVolumeID(vol.ID), "error", err)
		}
	}

	if err := s.DB.DeleteVolumesByTeam(ctx, teamID); err != nil {
		slog.Warn("team delete: failed to delete volume records", "team_id", id.FormatTeamID(teamID), "error", err)
	}
}

// GetMembers returns all members of the team with their emails and roles.
func (s *TeamService) GetMembers(ctx context.Context, teamID pgtype.UUID) ([]MemberInfo, error) {
	rows, err := s.DB.GetTeamMembers(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("get members: %w", err)
	}
	members := make([]MemberInfo, len(rows))
	for i, r := range rows {
		var joinedAt time.Time
		if r.JoinedAt.Valid {
			joinedAt = r.JoinedAt.Time
		}
		members[i] = MemberInfo{
			UserID:   id.FormatUserID(r.ID),
			Name:     r.Name,
			Email:    r.Email,
			Role:     r.Role,
			JoinedAt: joinedAt,
		}
	}
	return members, nil
}

// AddMember adds an existing user (looked up by email) to the team as a member.
// Caller must be admin or owner (verified from DB).
func (s *TeamService) AddMember(ctx context.Context, teamID, callerUserID pgtype.UUID, email string) (MemberInfo, error) {
	role, err := s.callerRole(ctx, teamID, callerUserID)
	if err != nil {
		return MemberInfo{}, err
	}
	if err := requireAdmin(role); err != nil {
		return MemberInfo{}, err
	}

	target, err := s.DB.GetUserByEmail(ctx, email)
	if err != nil {
		if err == pgx.ErrNoRows {
			return MemberInfo{}, apperr.UserNotFound.Msg("No account exists with that email.")
		}
		return MemberInfo{}, fmt.Errorf("look up user: %w", err)
	}

	// Check if already a member.
	_, memberCheckErr := s.DB.GetTeamMembership(ctx, db.GetTeamMembershipParams{
		UserID: target.ID,
		TeamID: teamID,
	})
	if memberCheckErr == nil {
		return MemberInfo{}, apperr.Conflict.Msg("This user is already a member of the team.")
	} else if memberCheckErr != pgx.ErrNoRows {
		return MemberInfo{}, fmt.Errorf("check membership: %w", memberCheckErr)
	}

	if err := s.DB.InsertTeamMember(ctx, db.InsertTeamMemberParams{
		UserID:    target.ID,
		TeamID:    teamID,
		IsDefault: false,
		Role:      "member",
	}); err != nil {
		return MemberInfo{}, fmt.Errorf("insert member: %w", err)
	}

	return MemberInfo{UserID: id.FormatUserID(target.ID), Name: target.Name, Email: target.Email, Role: "member"}, nil
}

// RemoveMember removes a user from the team.
// Caller must be admin or owner (verified from DB). Owner cannot be removed.
func (s *TeamService) RemoveMember(ctx context.Context, teamID, callerUserID, targetUserID pgtype.UUID) error {
	callerRole, err := s.callerRole(ctx, teamID, callerUserID)
	if err != nil {
		return err
	}
	if err := requireAdmin(callerRole); err != nil {
		return err
	}

	targetMembership, err := s.DB.GetTeamMembership(ctx, db.GetTeamMembershipParams{
		UserID: targetUserID,
		TeamID: teamID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return apperr.NotFound.Msg("This user is not a member of the team.")
		}
		return fmt.Errorf("get target membership: %w", err)
	}

	if targetMembership.Role == "owner" {
		return apperr.Forbidden.Msg("The owner cannot be removed from the team.")
	}

	if err := s.DB.DeleteTeamMember(ctx, db.DeleteTeamMemberParams{
		TeamID: teamID,
		UserID: targetUserID,
	}); err != nil {
		return fmt.Errorf("delete member: %w", err)
	}

	// Revoke the removed member's standing access to this team immediately.
	// Deleting the membership row alone leaves two credentials live: the
	// team-scoped API keys they created, and any cached session still holding
	// this team_id. Purge the keys, and drop the session cache so the next
	// request rehydrates from Postgres (where the membership is now gone) —
	// hydrateFromDB then clears the stale team binding.
	if err := s.DB.DeleteAPIKeysByTeamAndCreator(ctx, db.DeleteAPIKeysByTeamAndCreatorParams{
		TeamID:    teamID,
		CreatedBy: targetUserID,
	}); err != nil {
		slog.Warn("failed to delete API keys for removed member",
			"team_id", teamID, "user_id", targetUserID, "error", err)
	}
	if s.Sessions != nil {
		if err := s.Sessions.InvalidateCacheForUser(ctx, targetUserID); err != nil {
			slog.Warn("failed to invalidate session cache for removed member",
				"user_id", targetUserID, "error", err)
		}
	}
	return nil
}

// UpdateMemberRole changes a member's role to admin or member.
// Caller must be admin or owner (verified from DB). Owner's role cannot be changed.
// Valid target roles: "admin", "member".
func (s *TeamService) UpdateMemberRole(ctx context.Context, teamID, callerUserID, targetUserID pgtype.UUID, newRole string) error {
	if newRole != "admin" && newRole != "member" {
		return apperr.ValidationFailed.Msg("Role must be either admin or member.").With("field", "role")
	}

	callerRole, err := s.callerRole(ctx, teamID, callerUserID)
	if err != nil {
		return err
	}
	if err := requireAdmin(callerRole); err != nil {
		return err
	}

	targetMembership, err := s.DB.GetTeamMembership(ctx, db.GetTeamMembershipParams{
		UserID: targetUserID,
		TeamID: teamID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return apperr.NotFound.Msg("This user is not a member of the team.")
		}
		return fmt.Errorf("get target membership: %w", err)
	}

	if targetMembership.Role == "owner" {
		return apperr.Forbidden.Msg("The owner's role cannot be changed.")
	}

	if err := s.DB.UpdateMemberRole(ctx, db.UpdateMemberRoleParams{
		TeamID: teamID,
		UserID: targetUserID,
		Role:   newRole,
	}); err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	return nil
}

// LeaveTeam removes the calling user from the team.
// The owner cannot leave; they must delete the team instead.
func (s *TeamService) LeaveTeam(ctx context.Context, teamID, callerUserID pgtype.UUID) error {
	role, err := s.callerRole(ctx, teamID, callerUserID)
	if err != nil {
		return err
	}
	if role == "owner" {
		return apperr.Forbidden.Msg("The owner cannot leave the team; delete the team instead.")
	}

	if err := s.DB.DeleteTeamMember(ctx, db.DeleteTeamMemberParams{
		TeamID: teamID,
		UserID: callerUserID,
	}); err != nil {
		return fmt.Errorf("leave team: %w", err)
	}

	// Revoke the departing member's standing access to this team immediately,
	// exactly as RemoveMember does. Deleting the membership row alone leaves two
	// credentials live: the team-scoped API keys they created (resolved by hash
	// with no membership re-check, never expiring), and any cached session still
	// holding this team_id. Purge the keys, and drop the session cache so the
	// next request rehydrates from Postgres (where the membership is now gone).
	if err := s.DB.DeleteAPIKeysByTeamAndCreator(ctx, db.DeleteAPIKeysByTeamAndCreatorParams{
		TeamID:    teamID,
		CreatedBy: callerUserID,
	}); err != nil {
		slog.Warn("failed to delete API keys for departing member",
			"team_id", teamID, "user_id", callerUserID, "error", err)
	}
	if s.Sessions != nil {
		if err := s.Sessions.InvalidateCacheForUser(ctx, callerUserID); err != nil {
			slog.Warn("failed to invalidate session cache for departing member",
				"user_id", callerUserID, "error", err)
		}
	}
	return nil
}

// SetBYOC enables the BYOC feature flag for a team. Once enabled, BYOC cannot
// be disabled — it is a one-way transition.
// Admin-only — the caller must verify admin status before invoking this.
func (s *TeamService) SetBYOC(ctx context.Context, teamID pgtype.UUID, enabled bool) error {
	team, err := s.DB.GetTeam(ctx, teamID)
	if err != nil {
		return apperr.TeamNotFound.Wrap(err)
	}
	if team.DeletedAt.Valid {
		return apperr.TeamNotFound.New()
	}
	if !enabled {
		return apperr.Conflict.Msg("BYOC cannot be disabled once enabled.")
	}
	if team.IsByoc {
		// Already enabled — idempotent, no-op.
		return nil
	}
	if err := s.DB.SetTeamBYOC(ctx, db.SetTeamBYOCParams{ID: teamID, IsByoc: true}); err != nil {
		return fmt.Errorf("set byoc: %w", err)
	}
	return nil
}

// AdminTeamRow is the shape returned by AdminListTeams.
type AdminTeamRow struct {
	ID                 pgtype.UUID
	Name               string
	Slug               string
	IsByoc             bool
	CreatedAt          time.Time
	DeletedAt          *time.Time
	MemberCount        int32
	OwnerName          string
	OwnerEmail         string
	ActiveSandboxCount int32
	ChannelCount       int32
	RunningVcpus       int32
	RunningMemoryMb    int32
}

// AdminListTeams returns a paginated list of all teams (excluding the platform
// team) with member counts, owner info, and active sandbox counts.
// Admin-only — caller must verify admin status.
func (s *TeamService) AdminListTeams(ctx context.Context, limit, offset int32) ([]AdminTeamRow, int32, error) {
	teams, err := s.DB.ListTeamsAdmin(ctx, db.ListTeamsAdminParams{
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("list teams: %w", err)
	}

	total, err := s.DB.CountTeamsAdmin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("count teams: %w", err)
	}

	rows := make([]AdminTeamRow, len(teams))
	for i, t := range teams {
		row := AdminTeamRow{
			ID:                 t.ID,
			Name:               t.Name,
			Slug:               t.Slug,
			IsByoc:             t.IsByoc,
			CreatedAt:          t.CreatedAt.Time,
			MemberCount:        t.MemberCount,
			OwnerName:          t.OwnerName,
			OwnerEmail:         t.OwnerEmail,
			ActiveSandboxCount: t.ActiveSandboxCount,
			ChannelCount:       t.ChannelCount,
			RunningVcpus:       t.RunningVcpus,
			RunningMemoryMb:    t.RunningMemoryMb,
		}
		if t.DeletedAt.Valid {
			deletedAt := t.DeletedAt.Time
			row.DeletedAt = &deletedAt
		}
		rows[i] = row
	}
	return rows, total, nil
}

// DeleteTeamInternal soft-deletes a team and destroys all its active sandboxes.
// Used for system-initiated deletions (e.g. cascading from user account deletion)
// where no caller role check is needed.
func (s *TeamService) DeleteTeamInternal(ctx context.Context, teamID pgtype.UUID) error {
	return s.deleteTeamCore(ctx, teamID)
}

// AdminDeleteTeam soft-deletes a team and destroys all its active sandboxes.
// Unlike DeleteTeam, this does not require the caller to be the team owner —
// it is admin-only (caller must verify admin status).
func (s *TeamService) AdminDeleteTeam(ctx context.Context, teamID pgtype.UUID) error {
	team, err := s.DB.GetTeam(ctx, teamID)
	if err != nil {
		return apperr.TeamNotFound.Wrap(err)
	}
	if team.DeletedAt.Valid {
		return apperr.TeamNotFound.New()
	}

	return s.deleteTeamCore(ctx, teamID)
}
