package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
)

// HostService provides host management operations.
type HostService struct {
	DB    *db.Queries
	Redis *redis.Client
	JWT   []byte
}

// HostCreateParams holds the parameters for creating a host.
type HostCreateParams struct {
	Type             string
	TeamID           string // required for BYOC, empty for regular
	Provider         string
	AvailabilityZone string
	RequestingUserID string
	IsRequestorAdmin bool
}

// HostCreateResult holds the created host and the one-time registration token.
type HostCreateResult struct {
	Host              db.Host
	RegistrationToken string
}

// HostRegisterParams holds the parameters for host agent registration.
type HostRegisterParams struct {
	Token    string
	Arch     string
	CPUCores int32
	MemoryMB int32
	DiskGB   int32
	Address  string
}

// HostRegisterResult holds the registered host and its long-lived JWT.
type HostRegisterResult struct {
	Host db.Host
	JWT  string
}

// regTokenPayload is the JSON stored in Redis for registration tokens.
type regTokenPayload struct {
	HostID  string `json:"host_id"`
	TokenID string `json:"token_id"`
}

const regTokenTTL = time.Hour

// Create creates a new host record and generates a one-time registration token.
func (s *HostService) Create(ctx context.Context, p HostCreateParams) (HostCreateResult, error) {
	if p.Type != "regular" && p.Type != "byoc" {
		return HostCreateResult{}, fmt.Errorf("invalid host type: must be 'regular' or 'byoc'")
	}

	if p.Type == "regular" {
		if !p.IsRequestorAdmin {
			return HostCreateResult{}, fmt.Errorf("forbidden: only admins can create regular hosts")
		}
	} else {
		// BYOC: admin or team owner.
		if p.TeamID == "" {
			return HostCreateResult{}, fmt.Errorf("invalid request: team_id is required for BYOC hosts")
		}
		if !p.IsRequestorAdmin {
			membership, err := s.DB.GetTeamMembership(ctx, db.GetTeamMembershipParams{
				UserID: p.RequestingUserID,
				TeamID: p.TeamID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return HostCreateResult{}, fmt.Errorf("forbidden: not a member of the specified team")
			}
			if err != nil {
				return HostCreateResult{}, fmt.Errorf("check team membership: %w", err)
			}
			if membership.Role != "owner" {
				return HostCreateResult{}, fmt.Errorf("forbidden: only team owners can create BYOC hosts")
			}
		}
	}

	// Validate team exists and is not deleted for BYOC hosts.
	if p.TeamID != "" {
		team, err := s.DB.GetTeam(ctx, p.TeamID)
		if err != nil || team.DeletedAt.Valid {
			return HostCreateResult{}, fmt.Errorf("invalid request: team not found")
		}
	}

	hostID := id.NewHostID()

	var teamID pgtype.Text
	if p.TeamID != "" {
		teamID = pgtype.Text{String: p.TeamID, Valid: true}
	}
	var provider pgtype.Text
	if p.Provider != "" {
		provider = pgtype.Text{String: p.Provider, Valid: true}
	}
	var az pgtype.Text
	if p.AvailabilityZone != "" {
		az = pgtype.Text{String: p.AvailabilityZone, Valid: true}
	}

	host, err := s.DB.InsertHost(ctx, db.InsertHostParams{
		ID:               hostID,
		Type:             p.Type,
		TeamID:           teamID,
		Provider:         provider,
		AvailabilityZone: az,
		CreatedBy:        p.RequestingUserID,
	})
	if err != nil {
		return HostCreateResult{}, fmt.Errorf("insert host: %w", err)
	}

	// Generate registration token and store in Redis + Postgres audit trail.
	token := id.NewRegistrationToken()
	tokenID := id.NewHostTokenID()

	payload, _ := json.Marshal(regTokenPayload{
		HostID:  hostID,
		TokenID: tokenID,
	})
	if err := s.Redis.Set(ctx, "host:reg:"+token, payload, regTokenTTL).Err(); err != nil {
		return HostCreateResult{}, fmt.Errorf("store registration token: %w", err)
	}

	now := time.Now()
	if _, err := s.DB.InsertHostToken(ctx, db.InsertHostTokenParams{
		ID:        tokenID,
		HostID:    hostID,
		CreatedBy: p.RequestingUserID,
		ExpiresAt: pgtype.Timestamptz{Time: now.Add(regTokenTTL), Valid: true},
	}); err != nil {
		slog.Warn("failed to insert host token audit record", "host_id", hostID, "error", err)
	}

	return HostCreateResult{Host: host, RegistrationToken: token}, nil
}

// RegenerateToken issues a new registration token for a host still in "pending"
// status. This allows retry when a previous registration attempt failed after
// the original token was consumed.
func (s *HostService) RegenerateToken(ctx context.Context, hostID, userID, teamID string, isAdmin bool) (HostCreateResult, error) {
	host, err := s.DB.GetHost(ctx, hostID)
	if err != nil {
		return HostCreateResult{}, fmt.Errorf("host not found: %w", err)
	}
	if host.Status != "pending" {
		return HostCreateResult{}, fmt.Errorf("invalid state: can only regenerate token for pending hosts (status: %s)", host.Status)
	}

	// Same permission model as Create/Delete.
	if !isAdmin {
		if host.Type != "byoc" {
			return HostCreateResult{}, fmt.Errorf("forbidden: only admins can manage regular hosts")
		}
		if !host.TeamID.Valid || host.TeamID.String != teamID {
			return HostCreateResult{}, fmt.Errorf("forbidden: host does not belong to your team")
		}
		membership, err := s.DB.GetTeamMembership(ctx, db.GetTeamMembershipParams{
			UserID: userID,
			TeamID: teamID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return HostCreateResult{}, fmt.Errorf("forbidden: not a member of the specified team")
		}
		if err != nil {
			return HostCreateResult{}, fmt.Errorf("check team membership: %w", err)
		}
		if membership.Role != "owner" {
			return HostCreateResult{}, fmt.Errorf("forbidden: only team owners can regenerate tokens")
		}
	}

	token := id.NewRegistrationToken()
	tokenID := id.NewHostTokenID()

	payload, _ := json.Marshal(regTokenPayload{
		HostID:  hostID,
		TokenID: tokenID,
	})
	if err := s.Redis.Set(ctx, "host:reg:"+token, payload, regTokenTTL).Err(); err != nil {
		return HostCreateResult{}, fmt.Errorf("store registration token: %w", err)
	}

	now := time.Now()
	if _, err := s.DB.InsertHostToken(ctx, db.InsertHostTokenParams{
		ID:        tokenID,
		HostID:    hostID,
		CreatedBy: userID,
		ExpiresAt: pgtype.Timestamptz{Time: now.Add(regTokenTTL), Valid: true},
	}); err != nil {
		slog.Warn("failed to insert host token audit record", "host_id", hostID, "error", err)
	}

	return HostCreateResult{Host: host, RegistrationToken: token}, nil
}

// Register validates a one-time registration token, updates the host with
// machine specs, and returns a long-lived host JWT.
func (s *HostService) Register(ctx context.Context, p HostRegisterParams) (HostRegisterResult, error) {
	// Atomic consume: GetDel returns the value and deletes in one operation,
	// preventing concurrent requests from consuming the same token.
	raw, err := s.Redis.GetDel(ctx, "host:reg:"+p.Token).Bytes()
	if err == redis.Nil {
		return HostRegisterResult{}, fmt.Errorf("invalid or expired registration token")
	}
	if err != nil {
		return HostRegisterResult{}, fmt.Errorf("token lookup: %w", err)
	}

	var payload regTokenPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return HostRegisterResult{}, fmt.Errorf("corrupted registration token")
	}

	if _, err := s.DB.GetHost(ctx, payload.HostID); err != nil {
		return HostRegisterResult{}, fmt.Errorf("host not found: %w", err)
	}

	// Sign JWT before mutating DB — if signing fails, the host stays pending.
	hostJWT, err := auth.SignHostJWT(s.JWT, payload.HostID)
	if err != nil {
		return HostRegisterResult{}, fmt.Errorf("sign host token: %w", err)
	}

	// Atomically update only if still pending (defense-in-depth against races).
	rowsAffected, err := s.DB.RegisterHost(ctx, db.RegisterHostParams{
		ID:       payload.HostID,
		Arch:     pgtype.Text{String: p.Arch, Valid: p.Arch != ""},
		CpuCores: pgtype.Int4{Int32: p.CPUCores, Valid: p.CPUCores > 0},
		MemoryMb: pgtype.Int4{Int32: p.MemoryMB, Valid: p.MemoryMB > 0},
		DiskGb:   pgtype.Int4{Int32: p.DiskGB, Valid: p.DiskGB > 0},
		Address:  pgtype.Text{String: p.Address, Valid: p.Address != ""},
	})
	if err != nil {
		return HostRegisterResult{}, fmt.Errorf("register host: %w", err)
	}
	if rowsAffected == 0 {
		return HostRegisterResult{}, fmt.Errorf("host already registered or not found")
	}

	// Mark audit trail.
	if err := s.DB.MarkHostTokenUsed(ctx, payload.TokenID); err != nil {
		slog.Warn("failed to mark host token used", "token_id", payload.TokenID, "error", err)
	}

	// Re-fetch the host to get the updated state.
	host, err := s.DB.GetHost(ctx, payload.HostID)
	if err != nil {
		return HostRegisterResult{}, fmt.Errorf("fetch updated host: %w", err)
	}

	return HostRegisterResult{Host: host, JWT: hostJWT}, nil
}

// Heartbeat updates the last heartbeat timestamp for a host.
func (s *HostService) Heartbeat(ctx context.Context, hostID string) error {
	return s.DB.UpdateHostHeartbeat(ctx, hostID)
}

// List returns hosts visible to the caller.
// Admins see all hosts; non-admins see only BYOC hosts belonging to their team.
func (s *HostService) List(ctx context.Context, teamID string, isAdmin bool) ([]db.Host, error) {
	if isAdmin {
		return s.DB.ListHosts(ctx)
	}
	return s.DB.ListHostsByTeam(ctx, pgtype.Text{String: teamID, Valid: true})
}

// Get returns a single host, enforcing access control.
func (s *HostService) Get(ctx context.Context, hostID, teamID string, isAdmin bool) (db.Host, error) {
	host, err := s.DB.GetHost(ctx, hostID)
	if err != nil {
		return db.Host{}, fmt.Errorf("host not found: %w", err)
	}
	if !isAdmin {
		if !host.TeamID.Valid || host.TeamID.String != teamID {
			return db.Host{}, fmt.Errorf("host not found")
		}
	}
	return host, nil
}

// Delete removes a host. Admins can delete any host. Team owners can delete
// BYOC hosts belonging to their team.
func (s *HostService) Delete(ctx context.Context, hostID, userID, teamID string, isAdmin bool) error {
	host, err := s.DB.GetHost(ctx, hostID)
	if err != nil {
		return fmt.Errorf("host not found: %w", err)
	}

	if !isAdmin {
		if host.Type != "byoc" {
			return fmt.Errorf("forbidden: only admins can delete regular hosts")
		}
		if !host.TeamID.Valid || host.TeamID.String != teamID {
			return fmt.Errorf("forbidden: host does not belong to your team")
		}
		membership, err := s.DB.GetTeamMembership(ctx, db.GetTeamMembershipParams{
			UserID: userID,
			TeamID: teamID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("forbidden: not a member of the specified team")
		}
		if err != nil {
			return fmt.Errorf("check team membership: %w", err)
		}
		if membership.Role != "owner" {
			return fmt.Errorf("forbidden: only team owners can delete BYOC hosts")
		}
	}

	return s.DB.DeleteHost(ctx, hostID)
}

// AddTag adds a tag to a host.
func (s *HostService) AddTag(ctx context.Context, hostID, teamID string, isAdmin bool, tag string) error {
	if _, err := s.Get(ctx, hostID, teamID, isAdmin); err != nil {
		return err
	}
	return s.DB.AddHostTag(ctx, db.AddHostTagParams{HostID: hostID, Tag: tag})
}

// RemoveTag removes a tag from a host.
func (s *HostService) RemoveTag(ctx context.Context, hostID, teamID string, isAdmin bool, tag string) error {
	if _, err := s.Get(ctx, hostID, teamID, isAdmin); err != nil {
		return err
	}
	return s.DB.RemoveHostTag(ctx, db.RemoveHostTagParams{HostID: hostID, Tag: tag})
}

// ListTags returns all tags for a host.
func (s *HostService) ListTags(ctx context.Context, hostID, teamID string, isAdmin bool) ([]string, error) {
	if _, err := s.Get(ctx, hostID, teamID, isAdmin); err != nil {
		return nil, err
	}
	return s.DB.GetHostTags(ctx, hostID)
}
