package service

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
)

var teamNameRE = regexp.MustCompile(`^[A-Za-z0-9 _\-@']{1,128}$`)

// TeamService provides team management operations.
type TeamService struct {
	DB         *db.Queries
	Pool       *pgxpool.Pool
	HostPool   *lifecycle.HostClientPool
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
func (s *TeamService) callerRole(ctx context.Context, teamID, callerUserID string) (string, error) {
	m, err := s.DB.GetTeamMembership(ctx, db.GetTeamMembershipParams{
		UserID: callerUserID,
		TeamID: teamID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", fmt.Errorf("forbidden: not a member of this team")
		}
		return "", fmt.Errorf("get membership: %w", err)
	}
	return m.Role, nil
}

// requireAdmin returns an error if the caller is not an admin or owner.
func requireAdmin(role string) error {
	if role != "owner" && role != "admin" {
		return fmt.Errorf("forbidden: admin or owner role required")
	}
	return nil
}

// GetTeam returns the team by ID. Returns an error if the team is deleted or not found.
func (s *TeamService) GetTeam(ctx context.Context, teamID string) (db.Team, error) {
	team, err := s.DB.GetTeam(ctx, teamID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return db.Team{}, fmt.Errorf("team not found")
		}
		return db.Team{}, fmt.Errorf("get team: %w", err)
	}
	if team.DeletedAt.Valid {
		return db.Team{}, fmt.Errorf("team not found")
	}
	return team, nil
}

// ListTeamsForUser returns all active teams the user belongs to, with their role in each.
func (s *TeamService) ListTeamsForUser(ctx context.Context, userID string) ([]TeamWithRole, error) {
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
func (s *TeamService) CreateTeam(ctx context.Context, ownerUserID, name string) (TeamWithRole, error) {
	if !teamNameRE.MatchString(name) {
		return TeamWithRole{}, fmt.Errorf("invalid team name: must be 1-128 characters, A-Z a-z 0-9 space _")
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
func (s *TeamService) RenameTeam(ctx context.Context, teamID, callerUserID, newName string) error {
	if !teamNameRE.MatchString(newName) {
		return fmt.Errorf("invalid team name: must be 1-128 characters, A-Z a-z 0-9 space _")
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

// DeleteTeam soft-deletes the team and destroys all running/paused/starting sandboxes.
// Caller must be owner (verified from DB). All DB records (sandboxes, keys, templates)
// are preserved; only the team's deleted_at is set and active VMs are stopped.
func (s *TeamService) DeleteTeam(ctx context.Context, teamID, callerUserID string) error {
	role, err := s.callerRole(ctx, teamID, callerUserID)
	if err != nil {
		return err
	}
	if role != "owner" {
		return fmt.Errorf("forbidden: only the owner can delete a team")
	}

	// Collect active sandboxes and stop them.
	sandboxes, err := s.DB.ListActiveSandboxesByTeam(ctx, teamID)
	if err != nil {
		return fmt.Errorf("list active sandboxes: %w", err)
	}

	var stopIDs []string
	for _, sb := range sandboxes {
		host, hostErr := s.DB.GetHost(ctx, sb.HostID)
		if hostErr == nil {
			agent, agentErr := s.HostPool.GetForHost(host)
			if agentErr == nil {
				if _, err := agent.DestroySandbox(ctx, connect.NewRequest(&pb.DestroySandboxRequest{
					SandboxId: sb.ID,
				})); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
					slog.Warn("team delete: failed to destroy sandbox", "sandbox_id", sb.ID, "error", err)
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
	}

	if err := s.DB.SoftDeleteTeam(ctx, teamID); err != nil {
		return fmt.Errorf("soft delete team: %w", err)
	}
	return nil
}

// GetMembers returns all members of the team with their emails and roles.
func (s *TeamService) GetMembers(ctx context.Context, teamID string) ([]MemberInfo, error) {
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
			UserID:   r.ID,
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
func (s *TeamService) AddMember(ctx context.Context, teamID, callerUserID, email string) (MemberInfo, error) {
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
			return MemberInfo{}, fmt.Errorf("user not found: no account with that email")
		}
		return MemberInfo{}, fmt.Errorf("look up user: %w", err)
	}

	// Check if already a member.
	_, memberCheckErr := s.DB.GetTeamMembership(ctx, db.GetTeamMembershipParams{
		UserID: target.ID,
		TeamID: teamID,
	})
	if memberCheckErr == nil {
		return MemberInfo{}, fmt.Errorf("invalid: user is already a member of this team")
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

	return MemberInfo{UserID: target.ID, Name: target.Name, Email: target.Email, Role: "member"}, nil
}

// RemoveMember removes a user from the team.
// Caller must be admin or owner (verified from DB). Owner cannot be removed.
func (s *TeamService) RemoveMember(ctx context.Context, teamID, callerUserID, targetUserID string) error {
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
			return fmt.Errorf("not found: user is not a member of this team")
		}
		return fmt.Errorf("get target membership: %w", err)
	}

	if targetMembership.Role == "owner" {
		return fmt.Errorf("forbidden: the owner cannot be removed from the team")
	}

	if err := s.DB.DeleteTeamMember(ctx, db.DeleteTeamMemberParams{
		TeamID: teamID,
		UserID: targetUserID,
	}); err != nil {
		return fmt.Errorf("delete member: %w", err)
	}
	return nil
}

// UpdateMemberRole changes a member's role to admin or member.
// Caller must be admin or owner (verified from DB). Owner's role cannot be changed.
// Valid target roles: "admin", "member".
func (s *TeamService) UpdateMemberRole(ctx context.Context, teamID, callerUserID, targetUserID, newRole string) error {
	if newRole != "admin" && newRole != "member" {
		return fmt.Errorf("invalid: role must be admin or member")
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
			return fmt.Errorf("not found: user is not a member of this team")
		}
		return fmt.Errorf("get target membership: %w", err)
	}

	if targetMembership.Role == "owner" {
		return fmt.Errorf("forbidden: the owner's role cannot be changed")
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
func (s *TeamService) LeaveTeam(ctx context.Context, teamID, callerUserID string) error {
	role, err := s.callerRole(ctx, teamID, callerUserID)
	if err != nil {
		return err
	}
	if role == "owner" {
		return fmt.Errorf("forbidden: the owner cannot leave the team; delete the team instead")
	}

	if err := s.DB.DeleteTeamMember(ctx, db.DeleteTeamMemberParams{
		TeamID: teamID,
		UserID: callerUserID,
	}); err != nil {
		return fmt.Errorf("leave team: %w", err)
	}
	return nil
}

// SearchUsersByEmailPrefix returns up to 10 users whose email starts with the given prefix.
// The prefix must contain "@" to prevent broad enumeration.
func (s *TeamService) SearchUsersByEmailPrefix(ctx context.Context, prefix string) ([]db.SearchUsersByEmailPrefixRow, error) {
	return s.DB.SearchUsersByEmailPrefix(ctx, pgtype.Text{String: prefix, Valid: true})
}
