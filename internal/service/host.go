package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
)

// HostService provides host management operations.
type HostService struct {
	DB    *db.Queries
	Redis *redis.Client
	JWT   []byte
	Pool  *lifecycle.HostClientPool
}

// HostCreateParams holds the parameters for creating a host.
type HostCreateParams struct {
	Type             string
	TeamID           pgtype.UUID // required for BYOC, zero value for regular
	Provider         string
	AvailabilityZone string
	RequestingUserID pgtype.UUID
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

// HostRegisterResult holds the registered host, its short-lived JWT, and a long-lived refresh token.
type HostRegisterResult struct {
	Host         db.Host
	JWT          string
	RefreshToken string
}

// HostRefreshResult holds a new JWT and rotated refresh token after a successful refresh.
type HostRefreshResult struct {
	Host         db.Host
	JWT          string
	RefreshToken string
}

// HostDeletePreview describes what will be affected by deleting a host.
type HostDeletePreview struct {
	Host       db.Host
	SandboxIDs []string
}

// regTokenPayload is the JSON stored in Redis for registration tokens.
type regTokenPayload struct {
	HostID  string `json:"host_id"`
	TokenID string `json:"token_id"`
}

const regTokenTTL = time.Hour

// requireAdminOrOwner returns nil iff the role is "owner" or "admin".
func requireAdminOrOwner(role string) error {
	if role == "owner" || role == "admin" {
		return nil
	}
	return fmt.Errorf("forbidden: only team owners and admins can manage BYOC hosts")
}

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
		// BYOC: platform admin, or team owner/admin.
		if !p.TeamID.Valid {
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
			if err := requireAdminOrOwner(membership.Role); err != nil {
				return HostCreateResult{}, err
			}
		}
	}

	// Validate team exists, is not deleted, and has BYOC enabled.
	if p.TeamID.Valid {
		team, err := s.DB.GetTeam(ctx, p.TeamID)
		if err != nil || team.DeletedAt.Valid {
			return HostCreateResult{}, fmt.Errorf("invalid request: team not found")
		}
		if !team.IsByoc {
			return HostCreateResult{}, fmt.Errorf("forbidden: BYOC is not enabled for this team")
		}
	}

	hostID := id.NewHostID()

	host, err := s.DB.InsertHost(ctx, db.InsertHostParams{
		ID:               hostID,
		Type:             p.Type,
		TeamID:           p.TeamID,
		Provider:         p.Provider,
		AvailabilityZone: p.AvailabilityZone,
		CreatedBy:        p.RequestingUserID,
	})
	if err != nil {
		return HostCreateResult{}, fmt.Errorf("insert host: %w", err)
	}

	// Generate registration token and store in Redis + Postgres audit trail.
	token := id.NewRegistrationToken()
	tokenID := id.NewHostTokenID()

	payload, _ := json.Marshal(regTokenPayload{
		HostID:  id.FormatHostID(hostID),
		TokenID: id.FormatHostTokenID(tokenID),
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
		slog.Warn("failed to insert host token audit record", "host_id", id.FormatHostID(hostID), "error", err)
	}

	return HostCreateResult{Host: host, RegistrationToken: token}, nil
}

// RegenerateToken issues a new registration token for a host still in "pending"
// status. This allows retry when a previous registration attempt failed after
// the original token was consumed.
func (s *HostService) RegenerateToken(ctx context.Context, hostID, userID, teamID pgtype.UUID, isAdmin bool) (HostCreateResult, error) {
	host, err := s.DB.GetHost(ctx, hostID)
	if err != nil {
		return HostCreateResult{}, fmt.Errorf("host not found: %w", err)
	}
	if host.Status != "pending" {
		return HostCreateResult{}, fmt.Errorf("invalid state: can only regenerate token for pending hosts (status: %s)", host.Status)
	}

	if !isAdmin {
		if host.Type != "byoc" {
			return HostCreateResult{}, fmt.Errorf("forbidden: only admins can manage regular hosts")
		}
		if !host.TeamID.Valid || host.TeamID != teamID {
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
		if err := requireAdminOrOwner(membership.Role); err != nil {
			return HostCreateResult{}, err
		}
	}

	token := id.NewRegistrationToken()
	tokenID := id.NewHostTokenID()

	payload, _ := json.Marshal(regTokenPayload{
		HostID:  id.FormatHostID(hostID),
		TokenID: id.FormatHostTokenID(tokenID),
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
		slog.Warn("failed to insert host token audit record", "host_id", id.FormatHostID(hostID), "error", err)
	}

	return HostCreateResult{Host: host, RegistrationToken: token}, nil
}

// Register validates a one-time registration token, updates the host with
// machine specs, and returns a short-lived host JWT plus a long-lived refresh token.
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

	hostID, err := id.ParseHostID(payload.HostID)
	if err != nil {
		return HostRegisterResult{}, fmt.Errorf("corrupted registration token: %w", err)
	}
	tokenID, err := id.ParseHostTokenID(payload.TokenID)
	if err != nil {
		return HostRegisterResult{}, fmt.Errorf("corrupted registration token: %w", err)
	}

	if _, err := s.DB.GetHost(ctx, hostID); err != nil {
		return HostRegisterResult{}, fmt.Errorf("host not found: %w", err)
	}

	// Sign JWT before mutating DB — if signing fails, the host stays pending.
	hostJWT, err := auth.SignHostJWT(s.JWT, hostID)
	if err != nil {
		return HostRegisterResult{}, fmt.Errorf("sign host token: %w", err)
	}

	// Atomically update only if still pending (defense-in-depth against races).
	rowsAffected, err := s.DB.RegisterHost(ctx, db.RegisterHostParams{
		ID:       hostID,
		Arch:     p.Arch,
		CpuCores: p.CPUCores,
		MemoryMb: p.MemoryMB,
		DiskGb:   p.DiskGB,
		Address:  p.Address,
	})
	if err != nil {
		return HostRegisterResult{}, fmt.Errorf("register host: %w", err)
	}
	if rowsAffected == 0 {
		return HostRegisterResult{}, fmt.Errorf("host already registered or not found")
	}

	// Mark audit trail.
	if err := s.DB.MarkHostTokenUsed(ctx, tokenID); err != nil {
		slog.Warn("failed to mark host token used", "token_id", payload.TokenID, "error", err)
	}

	// Issue a long-lived refresh token.
	refreshToken, err := s.issueRefreshToken(ctx, hostID)
	if err != nil {
		return HostRegisterResult{}, fmt.Errorf("issue refresh token: %w", err)
	}

	// Re-fetch the host to get the updated state.
	host, err := s.DB.GetHost(ctx, hostID)
	if err != nil {
		return HostRegisterResult{}, fmt.Errorf("fetch updated host: %w", err)
	}

	return HostRegisterResult{Host: host, JWT: hostJWT, RefreshToken: refreshToken}, nil
}

// Refresh validates a refresh token, rotates it (revokes old, issues new),
// and returns a fresh JWT plus the new refresh token.
func (s *HostService) Refresh(ctx context.Context, refreshToken string) (HostRefreshResult, error) {
	hash := hashToken(refreshToken)

	row, err := s.DB.GetHostRefreshTokenByHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return HostRefreshResult{}, fmt.Errorf("invalid or expired refresh token")
	}
	if err != nil {
		return HostRefreshResult{}, fmt.Errorf("lookup refresh token: %w", err)
	}

	host, err := s.DB.GetHost(ctx, row.HostID)
	if err != nil {
		return HostRefreshResult{}, fmt.Errorf("host not found: %w", err)
	}

	// Sign new JWT.
	hostJWT, err := auth.SignHostJWT(s.JWT, host.ID)
	if err != nil {
		return HostRefreshResult{}, fmt.Errorf("sign host JWT: %w", err)
	}

	// Issue-then-revoke rotation: insert new token first so a crash between
	// the two DB calls leaves the host with two valid tokens rather than zero.
	newRefreshToken, err := s.issueRefreshToken(ctx, host.ID)
	if err != nil {
		return HostRefreshResult{}, fmt.Errorf("issue new refresh token: %w", err)
	}

	// Revoke old refresh token after the new one is safely persisted.
	if err := s.DB.RevokeHostRefreshToken(ctx, row.ID); err != nil {
		return HostRefreshResult{}, fmt.Errorf("revoke old refresh token: %w", err)
	}

	return HostRefreshResult{Host: host, JWT: hostJWT, RefreshToken: newRefreshToken}, nil
}

// issueRefreshToken creates a new refresh token record in the DB and returns
// the opaque token string.
func (s *HostService) issueRefreshToken(ctx context.Context, hostID pgtype.UUID) (string, error) {
	token := id.NewRefreshToken()
	hash := hashToken(token)
	now := time.Now()

	if _, err := s.DB.InsertHostRefreshToken(ctx, db.InsertHostRefreshTokenParams{
		ID:        id.NewRefreshTokenID(),
		HostID:    hostID,
		TokenHash: hash,
		ExpiresAt: pgtype.Timestamptz{Time: now.Add(auth.HostRefreshTokenExpiry), Valid: true},
	}); err != nil {
		return "", fmt.Errorf("insert refresh token: %w", err)
	}

	return token, nil
}

// hashToken returns the hex-encoded SHA-256 hash of the token.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", h)
}

// Heartbeat updates the last heartbeat timestamp for a host and transitions
// any 'unreachable' host back to 'online'. Returns a "host not found" error
// (which becomes 404) if the host record no longer exists (e.g., was deleted).
func (s *HostService) Heartbeat(ctx context.Context, hostID pgtype.UUID) error {
	n, err := s.DB.UpdateHostHeartbeatAndStatus(ctx, hostID)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("host not found")
	}
	return nil
}

// List returns hosts visible to the caller.
// Admins see all hosts; non-admins see only BYOC hosts belonging to their team.
func (s *HostService) List(ctx context.Context, teamID pgtype.UUID, isAdmin bool) ([]db.Host, error) {
	if isAdmin {
		return s.DB.ListHosts(ctx)
	}
	return s.DB.ListHostsByTeam(ctx, teamID)
}

// Get returns a single host, enforcing access control.
func (s *HostService) Get(ctx context.Context, hostID, teamID pgtype.UUID, isAdmin bool) (db.Host, error) {
	host, err := s.DB.GetHost(ctx, hostID)
	if err != nil {
		return db.Host{}, fmt.Errorf("host not found: %w", err)
	}
	if !isAdmin {
		if !host.TeamID.Valid || host.TeamID != teamID {
			return db.Host{}, fmt.Errorf("host not found")
		}
	}
	return host, nil
}

// DeletePreview returns what would be affected by deleting the host, without
// making any changes. Use this to show the user a confirmation prompt.
func (s *HostService) DeletePreview(ctx context.Context, hostID, teamID pgtype.UUID, isAdmin bool) (HostDeletePreview, error) {
	host, err := s.checkDeletePermission(ctx, hostID, pgtype.UUID{}, teamID, isAdmin)
	if err != nil {
		return HostDeletePreview{}, err
	}

	sandboxes, err := s.DB.ListSandboxesByHostAndStatus(ctx, db.ListSandboxesByHostAndStatusParams{
		HostID:  hostID,
		Column2: []string{"pending", "starting", "running", "missing"},
	})
	if err != nil {
		return HostDeletePreview{}, fmt.Errorf("list sandboxes: %w", err)
	}

	ids := make([]string, len(sandboxes))
	for i, sb := range sandboxes {
		ids[i] = id.FormatSandboxID(sb.ID)
	}

	return HostDeletePreview{Host: host, SandboxIDs: ids}, nil
}

// Delete removes a host. Without force it returns an error listing active
// sandboxes so the caller can present a confirmation. With force it gracefully
// destroys all running sandboxes before deleting the host record.
func (s *HostService) Delete(ctx context.Context, hostID, userID, teamID pgtype.UUID, isAdmin, force bool) error {
	host, err := s.checkDeletePermission(ctx, hostID, userID, teamID, isAdmin)
	if err != nil {
		return err
	}

	sandboxes, err := s.DB.ListSandboxesByHostAndStatus(ctx, db.ListSandboxesByHostAndStatusParams{
		HostID:  hostID,
		Column2: []string{"pending", "starting", "running", "missing"},
	})
	if err != nil {
		return fmt.Errorf("list sandboxes: %w", err)
	}

	if len(sandboxes) > 0 && !force {
		ids := make([]string, len(sandboxes))
		for i, sb := range sandboxes {
			ids[i] = id.FormatSandboxID(sb.ID)
		}
		return &HostHasSandboxesError{SandboxIDs: ids}
	}

	hostIDStr := id.FormatHostID(hostID)

	// Gracefully destroy running sandboxes and terminate the agent (best-effort).
	if host.Address != "" {
		agent, err := s.Pool.GetForHost(host)
		if err == nil {
			for _, sb := range sandboxes {
				if sb.Status == "running" || sb.Status == "starting" {
					_, rpcErr := agent.DestroySandbox(ctx, connect.NewRequest(&pb.DestroySandboxRequest{
						SandboxId: id.FormatSandboxID(sb.ID),
					}))
					if rpcErr != nil && connect.CodeOf(rpcErr) != connect.CodeNotFound {
						slog.Warn("delete host: failed to destroy sandbox on agent", "sandbox_id", id.FormatSandboxID(sb.ID), "error", rpcErr)
					}
				}
			}
			// Tell the agent to shut itself down immediately.
			if _, rpcErr := agent.Terminate(ctx, connect.NewRequest(&pb.TerminateRequest{})); rpcErr != nil {
				slog.Warn("delete host: failed to send Terminate to agent", "host_id", hostIDStr, "error", rpcErr)
			}
		}
	}

	// Mark all affected sandboxes as stopped in DB.
	if len(sandboxes) > 0 {
		sbIDs := make([]pgtype.UUID, len(sandboxes))
		for i, sb := range sandboxes {
			sbIDs[i] = sb.ID
		}
		if err := s.DB.BulkUpdateStatusByIDs(ctx, db.BulkUpdateStatusByIDsParams{
			Column1: sbIDs,
			Status:  "stopped",
		}); err != nil {
			slog.Warn("delete host: failed to mark sandboxes stopped", "host_id", hostIDStr, "error", err)
		}
	}

	// Revoke all refresh tokens for this host.
	if err := s.DB.RevokeHostRefreshTokensByHost(ctx, hostID); err != nil {
		slog.Warn("delete host: failed to revoke refresh tokens", "host_id", hostIDStr, "error", err)
	}

	// Evict the client from the pool so no further RPCs are sent.
	if s.Pool != nil {
		s.Pool.Evict(id.FormatHostID(hostID))
	}

	return s.DB.DeleteHost(ctx, hostID)
}

// checkDeletePermission verifies the caller has permission to delete the given
// host and returns the host record on success.
func (s *HostService) checkDeletePermission(ctx context.Context, hostID, userID, teamID pgtype.UUID, isAdmin bool) (db.Host, error) {
	host, err := s.DB.GetHost(ctx, hostID)
	if err != nil {
		return db.Host{}, fmt.Errorf("host not found: %w", err)
	}

	if isAdmin {
		return host, nil
	}

	if host.Type != "byoc" {
		return db.Host{}, fmt.Errorf("forbidden: only admins can delete regular hosts")
	}
	if !host.TeamID.Valid || host.TeamID != teamID {
		return db.Host{}, fmt.Errorf("forbidden: host does not belong to your team")
	}

	if userID.Valid {
		membership, err := s.DB.GetTeamMembership(ctx, db.GetTeamMembershipParams{
			UserID: userID,
			TeamID: teamID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Host{}, fmt.Errorf("forbidden: not a member of the specified team")
		}
		if err != nil {
			return db.Host{}, fmt.Errorf("check team membership: %w", err)
		}
		if err := requireAdminOrOwner(membership.Role); err != nil {
			return db.Host{}, err
		}
	}

	return host, nil
}

// AddTag adds a tag to a host.
func (s *HostService) AddTag(ctx context.Context, hostID, teamID pgtype.UUID, isAdmin bool, tag string) error {
	if _, err := s.Get(ctx, hostID, teamID, isAdmin); err != nil {
		return err
	}
	return s.DB.AddHostTag(ctx, db.AddHostTagParams{HostID: hostID, Tag: tag})
}

// RemoveTag removes a tag from a host.
func (s *HostService) RemoveTag(ctx context.Context, hostID, teamID pgtype.UUID, isAdmin bool, tag string) error {
	if _, err := s.Get(ctx, hostID, teamID, isAdmin); err != nil {
		return err
	}
	return s.DB.RemoveHostTag(ctx, db.RemoveHostTagParams{HostID: hostID, Tag: tag})
}

// ListTags returns all tags for a host.
func (s *HostService) ListTags(ctx context.Context, hostID, teamID pgtype.UUID, isAdmin bool) ([]string, error) {
	if _, err := s.Get(ctx, hostID, teamID, isAdmin); err != nil {
		return nil, err
	}
	return s.DB.GetHostTags(ctx, hostID)
}

// HostHasSandboxesError is returned by Delete when the host has active sandboxes
// and force was not set. The caller should present the list to the user and
// re-call Delete with force=true if they confirm.
type HostHasSandboxesError struct {
	SandboxIDs []string
}

func (e *HostHasSandboxesError) Error() string {
	return fmt.Sprintf("host has %d active sandbox(es): %v", len(e.SandboxIDs), e.SandboxIDs)
}
