package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/scheduler"
	"git.omukk.dev/wrenn/wrenn/pkg/validate"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

// SandboxEventPublisher writes sandbox lifecycle events to the Redis stream.
type SandboxEventPublisher func(ctx context.Context, event SandboxStateEvent)

// SandboxStateEvent is the event payload published to the Redis stream.
type SandboxStateEvent struct {
	Event     string            `json:"event"`
	SandboxID string            `json:"sandbox_id"`
	TeamID    string            `json:"team_id,omitempty"`
	HostID    string            `json:"host_id"`
	HostIP    string            `json:"host_ip,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Error     string            `json:"error,omitempty"`
	Timestamp int64             `json:"timestamp"`
}

// SandboxService provides sandbox lifecycle operations shared between the
// REST API and the dashboard.
type SandboxService struct {
	DB           *db.Queries
	Pool         *lifecycle.HostClientPool
	Scheduler    scheduler.HostScheduler
	PublishEvent SandboxEventPublisher
}

// SandboxCreateParams holds the parameters for creating a sandbox.
type SandboxCreateParams struct {
	TeamID     pgtype.UUID
	Template   string
	VCPUs      int32
	MemoryMB   int32
	TimeoutSec int32
	// Metadata holds user-supplied key/value labels attached at create-time.
	// Reserved system keys (kernel_version, etc.) are rejected by validation.
	Metadata map[string]string
}

// MinTimeoutSec mirrors pkg/sandbox.MinTimeoutSec. Sub-minute TTLs race
// the post-create startup window (DB insert → /init → memory loader); the
// agent silently clamps anyway, but the CP must clamp too so the DB record
// agrees with what the agent runs. 0 is preserved (no TTL).
const MinTimeoutSec int32 = 60

// clampTimeout normalises a caller-supplied TTL the same way the host agent
// does. Keep in sync with pkg/sandbox.clampTimeout.
func clampTimeout(timeoutSec int32) int32 {
	if timeoutSec <= 0 {
		return 0
	}
	if timeoutSec < MinTimeoutSec {
		return MinTimeoutSec
	}
	return timeoutSec
}

// agentForSandbox looks up the host for the given sandbox and returns a client.
func (s *SandboxService) agentForSandbox(ctx context.Context, sandboxID pgtype.UUID) (hostagentClient, db.Sandbox, error) {
	sb, err := s.DB.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, db.Sandbox{}, apperr.SandboxNotFound.Wrap(err)
	}
	agent, err := s.agentForHost(ctx, sb.HostID)
	if err != nil {
		return nil, db.Sandbox{}, err
	}
	return agent, sb, nil
}

// agentForHost returns the host client by host UUID, skipping the sandbox
// lookup. Used by callers that already have a db.Sandbox in hand.
func (s *SandboxService) agentForHost(ctx context.Context, hostID pgtype.UUID) (hostagentClient, error) {
	host, err := s.DB.GetHost(ctx, hostID)
	if err != nil {
		return nil, apperr.HostNotFound.Wrap(err)
	}
	agent, err := s.Pool.GetForHost(host)
	if err != nil {
		return nil, fmt.Errorf("get agent client: %w", err)
	}
	return agent, nil
}

func (s *SandboxService) publishEvent(ctx context.Context, event SandboxStateEvent) {
	if s.PublishEvent != nil {
		s.PublishEvent(ctx, event)
	}
}

// hostagentClient is a local alias to avoid the full package path in signatures.
type hostagentClient = interface {
	CreateSandbox(ctx context.Context, req *connect.Request[pb.CreateSandboxRequest]) (*connect.Response[pb.CreateSandboxResponse], error)
	DestroySandbox(ctx context.Context, req *connect.Request[pb.DestroySandboxRequest]) (*connect.Response[pb.DestroySandboxResponse], error)
	PauseSandbox(ctx context.Context, req *connect.Request[pb.PauseSandboxRequest]) (*connect.Response[pb.PauseSandboxResponse], error)
	ResumeSandbox(ctx context.Context, req *connect.Request[pb.ResumeSandboxRequest]) (*connect.Response[pb.ResumeSandboxResponse], error)
	PingSandbox(ctx context.Context, req *connect.Request[pb.PingSandboxRequest]) (*connect.Response[pb.PingSandboxResponse], error)
	GetSandboxMetrics(ctx context.Context, req *connect.Request[pb.GetSandboxMetricsRequest]) (*connect.Response[pb.GetSandboxMetricsResponse], error)
	FlushSandboxMetrics(ctx context.Context, req *connect.Request[pb.FlushSandboxMetricsRequest]) (*connect.Response[pb.FlushSandboxMetricsResponse], error)
	CreateSnapshot(ctx context.Context, req *connect.Request[pb.CreateSnapshotRequest]) (*connect.Response[pb.CreateSnapshotResponse], error)
}

// resolveTemplateRef resolves a parsed template reference to its owning
// template row, enforcing cross-team visibility. It never distinguishes
// "missing" from "not visible" — both yield TemplateNotFound so template
// existence does not leak across teams.
func (s *SandboxService) resolveTemplateRef(ctx context.Context, requesterTeamID pgtype.UUID, slug, name string) (db.Template, error) {
	ref := name
	if slug != "" {
		ref = slug + "/" + name
	}
	notFound := apperr.TemplateNotFound.Msgf("Template %q not found.", ref)

	// Unqualified: own team first, then platform.
	if slug == "" {
		tmpl, err := s.DB.ResolveTemplateForTeam(ctx, db.ResolveTemplateForTeamParams{Name: name, TeamID: requesterTeamID})
		if err != nil {
			return db.Template{}, notFound
		}
		return tmpl, nil
	}

	// Slug-qualified: resolve the owning team, then the visible template.
	owner, err := s.DB.GetTeamBySlug(ctx, slug)
	if err != nil {
		return db.Template{}, notFound
	}
	tmpl, err := s.DB.GetVisibleTemplateByTeamName(ctx, db.GetVisibleTemplateByTeamNameParams{
		TeamID:   owner.ID, // owning team
		Name:     name,
		TeamID_2: requesterTeamID, // requester (grants visibility to own templates)
	})
	if err != nil {
		return db.Template{}, notFound
	}
	return tmpl, nil
}

// Create creates a new sandbox asynchronously: picks a host, inserts a
// "starting" DB record, fires the agent RPC in a background goroutine, and
// returns the sandbox immediately. The background goroutine publishes a
// sandbox event to the Redis stream when the operation completes.
func (s *SandboxService) Create(ctx context.Context, p SandboxCreateParams) (db.Sandbox, error) {
	if p.Template == "" {
		p.Template = "minimal-ubuntu"
	}
	slugPart, namePart, err := validate.TemplateRef(p.Template)
	if err != nil {
		return db.Sandbox{}, apperr.ValidationFailed.Msgf("Invalid template reference: %v.", err)
	}
	if err := validate.Metadata(p.Metadata); err != nil {
		return db.Sandbox{}, apperr.ValidationFailed.Msgf("Invalid metadata: %v.", err)
	}
	if p.VCPUs <= 0 {
		p.VCPUs = 2
	}
	if p.MemoryMB <= 0 {
		p.MemoryMB = 2048
	}
	p.TimeoutSec = clampTimeout(p.TimeoutSec)

	if !p.TeamID.Valid {
		return db.Sandbox{}, apperr.InvalidRequest.Msg("A team_id is required.")
	}

	// Resolve the template reference → owning (teamID, templateID). Unqualified
	// names prefer the requesting team's own template, then fall back to
	// platform templates. A "<slug>/<name>" reference targets a specific team
	// and succeeds only if that template is public, owned by the requester, or
	// a platform template.
	tmpl, err := s.resolveTemplateRef(ctx, p.TeamID, slugPart, namePart)
	if err != nil {
		return db.Sandbox{}, err
	}
	templateTeamID := tmpl.TeamID
	templateID := tmpl.ID
	templateDefaultUser := tmpl.DefaultUser
	var templateDefaultEnv map[string]string
	if len(tmpl.DefaultEnv) > 0 {
		_ = json.Unmarshal(tmpl.DefaultEnv, &templateDefaultEnv)
	}
	if tmpl.Type == "snapshot" {
		p.VCPUs = tmpl.Vcpus
		p.MemoryMB = tmpl.MemoryMb
	}

	team, err := s.DB.GetTeam(ctx, p.TeamID)
	if err != nil {
		return db.Sandbox{}, apperr.TeamNotFound.Wrap(err)
	}

	host, err := s.Scheduler.SelectHost(ctx, p.TeamID, team.IsByoc, p.MemoryMB, 0)
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("select host: %w", err)
	}

	agent, err := s.Pool.GetForHost(host)
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("get agent client: %w", err)
	}

	sandboxID := id.NewSandboxID()
	sandboxIDStr := id.FormatSandboxID(sandboxID)
	hostIDStr := id.FormatHostID(host.ID)

	metaJSON := []byte("{}")
	if len(p.Metadata) > 0 {
		if metaJSON, err = json.Marshal(p.Metadata); err != nil {
			return db.Sandbox{}, fmt.Errorf("marshal metadata: %w", err)
		}
	}

	sb, err := s.DB.InsertSandbox(ctx, db.InsertSandboxParams{
		ID:             sandboxID,
		TeamID:         p.TeamID,
		HostID:         host.ID,
		Template:       p.Template,
		Status:         "starting",
		Vcpus:          p.VCPUs,
		MemoryMb:       p.MemoryMB,
		TimeoutSec:     p.TimeoutSec,
		DiskSizeMb:     0,
		TemplateID:     templateID,
		TemplateTeamID: templateTeamID,
		Metadata:       metaJSON,
	})
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("insert sandbox: %w", err)
	}

	teamIDStr := id.FormatTeamID(p.TeamID)
	s.publishStateChanged(ctx, sandboxIDStr, teamIDStr, hostIDStr, "", "starting")
	go s.createInBackground(sandboxID, sandboxIDStr, hostIDStr, teamIDStr, agent, p, templateTeamID, templateID, templateDefaultUser, templateDefaultEnv)

	return sb, nil
}

func (s *SandboxService) createInBackground(
	sandboxID pgtype.UUID, sandboxIDStr, hostIDStr, teamIDStr string,
	agent hostagentClient, p SandboxCreateParams,
	templateTeamID, templateID pgtype.UUID,
	defaultUser string, defaultEnv map[string]string,
) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	resp, err := agent.CreateSandbox(bgCtx, connect.NewRequest(&pb.CreateSandboxRequest{
		SandboxId:   sandboxIDStr,
		Template:    p.Template,
		TeamId:      id.UUIDString(templateTeamID),
		TemplateId:  id.UUIDString(templateID),
		Vcpus:       p.VCPUs,
		MemoryMb:    p.MemoryMB,
		TimeoutSec:  p.TimeoutSec,
		DiskSizeMb:  0,
		DefaultUser: defaultUser,
		DefaultEnv:  defaultEnv,
	}))
	if err != nil {
		slog.Warn("background create failed", "sandbox_id", sandboxIDStr, "error", err)
		errCtx, errCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer errCancel()
		if _, dbErr := s.DB.UpdateSandboxStatusIf(errCtx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: "starting", Status_2: "error",
		}); dbErr != nil {
			slog.Warn("failed to update sandbox to error after create failure", "id", sandboxIDStr, "error", dbErr)
		}
		s.publishEvent(errCtx, SandboxStateEvent{
			Event: "sandbox.failed", SandboxID: sandboxIDStr, TeamID: teamIDStr, HostID: hostIDStr,
			Error: err.Error(), Timestamp: time.Now().Unix(),
		})
		return
	}

	if resp.Msg.DiskSizeMb > 0 {
		if err := s.DB.UpdateSandboxDiskSize(bgCtx, db.UpdateSandboxDiskSizeParams{
			ID:         sandboxID,
			DiskSizeMb: resp.Msg.DiskSizeMb,
		}); err != nil {
			slog.Warn("failed to update sandbox disk size", "id", sandboxIDStr, "error", err)
		}
	}

	now := time.Now()
	if _, dbErr := s.DB.UpdateSandboxRunningIf(bgCtx, db.UpdateSandboxRunningIfParams{
		ID:     sandboxID,
		Status: "starting",
		HostIp: resp.Msg.HostIp,
		StartedAt: pgtype.Timestamptz{
			Time:  now,
			Valid: true,
		},
	}); dbErr != nil {
		slog.Warn("failed to update sandbox running after create", "id", sandboxIDStr, "error", dbErr)
	}

	s.persistSystemMetadata(bgCtx, sandboxID, sandboxIDStr, p.Metadata, resp.Msg.Metadata)

	s.publishEvent(bgCtx, SandboxStateEvent{
		Event: "sandbox.started", SandboxID: sandboxIDStr, TeamID: teamIDStr, HostID: hostIDStr,
		HostIP: resp.Msg.HostIp, Metadata: resp.Msg.Metadata,
		Timestamp: now.Unix(),
	})
}

// persistSystemMetadata merges agent-provided system metadata over the given
// user labels and writes the result to the sandbox row. System keys
// (kernel_version, etc.) are applied last so they always win — users can never
// override protected fields even if create-time validation were bypassed.
// userMeta may be nil. No-op when the agent returned no system metadata.
func (s *SandboxService) persistSystemMetadata(
	ctx context.Context, sandboxID pgtype.UUID, sandboxIDStr string,
	userMeta, systemMeta map[string]string,
) {
	if len(systemMeta) == 0 {
		return
	}
	merged := make(map[string]string, len(userMeta)+len(systemMeta))
	for k, v := range userMeta {
		merged[k] = v
	}
	for k, v := range systemMeta {
		merged[k] = v
	}
	metaJSON, _ := json.Marshal(merged)
	if err := s.DB.UpdateSandboxMetadata(ctx, db.UpdateSandboxMetadataParams{
		ID: sandboxID, Metadata: metaJSON,
	}); err != nil {
		slog.Warn("failed to store sandbox metadata", "id", sandboxIDStr, "error", err)
	}
}

// List returns active sandboxes (excludes stopped/error) belonging to the given team.
func (s *SandboxService) List(ctx context.Context, teamID pgtype.UUID) ([]db.Sandbox, error) {
	return s.DB.ListSandboxesByTeam(ctx, teamID)
}

// Get returns a single sandbox by ID, scoped to the given team.
func (s *SandboxService) Get(ctx context.Context, sandboxID, teamID pgtype.UUID) (db.Sandbox, error) {
	return s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
}

// Pause asynchronously pauses a running sandbox. The DB CAS from "running"
// to "pausing" is the authoritative gate against concurrent Pause/Destroy
// calls; if it loses, no agent RPC fires.
func (s *SandboxService) Pause(ctx context.Context, sandboxID, teamID pgtype.UUID) (db.Sandbox, error) {
	sb, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		return db.Sandbox{}, apperr.SandboxNotFound.Wrap(err)
	}
	if sb.Status == "paused" {
		return sb, nil
	}

	if _, err := s.DB.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
		ID: sandboxID, Status: "running", Status_2: "pausing",
	}); err != nil {
		return db.Sandbox{}, apperr.SandboxNotRunning.Wrap(err).With("status", sb.Status)
	}

	agent, err := s.agentForHost(ctx, sb.HostID)
	if err != nil {
		// Roll back the CAS so the sandbox isn't stuck in "pausing".
		if _, rerr := s.DB.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: "pausing", Status_2: "running",
		}); rerr != nil {
			slog.Warn("failed to roll back pausing→running", "id", id.FormatSandboxID(sandboxID), "error", rerr)
		}
		return db.Sandbox{}, err
	}

	sandboxIDStr := id.FormatSandboxID(sandboxID)
	hostIDStr := id.FormatHostID(sb.HostID)
	teamIDStr := id.FormatTeamID(sb.TeamID)

	go s.pauseInBackground(sandboxID, sandboxIDStr, hostIDStr, teamIDStr, agent)

	sb.Status = "pausing"
	return sb, nil
}

func (s *SandboxService) pauseInBackground(sandboxID pgtype.UUID, sandboxIDStr, hostIDStr, teamIDStr string, agent hostagentClient) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Flush metrics before the VM stops sampling so the persisted history
	// covers the entire run-up to the pause.
	s.flushAndPersistMetrics(bgCtx, agent, sandboxID, true)

	if _, err := agent.PauseSandbox(bgCtx, connect.NewRequest(&pb.PauseSandboxRequest{
		SandboxId: sandboxIDStr,
	})); err != nil {
		slog.Warn("background pause failed", "sandbox_id", sandboxIDStr, "error", err)
		// Best-effort: try to recover the sandbox back to "running" so the
		// user isn't stuck in "pausing".
		if _, dbErr := s.DB.UpdateSandboxStatusIf(bgCtx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: "pausing", Status_2: "running",
		}); dbErr != nil {
			slog.Warn("failed to recover pausing→running after pause failure", "id", sandboxIDStr, "error", dbErr)
		}
		s.publishEvent(bgCtx, SandboxStateEvent{
			Event: "sandbox.pause_failed", SandboxID: sandboxIDStr, TeamID: teamIDStr, HostID: hostIDStr,
			Error: err.Error(), Timestamp: time.Now().Unix(),
		})
		return
	}

	if _, err := s.DB.UpdateSandboxStatusIf(bgCtx, db.UpdateSandboxStatusIfParams{
		ID: sandboxID, Status: "pausing", Status_2: "paused",
	}); err != nil {
		slog.Warn("failed to update sandbox to paused", "sandbox_id", sandboxIDStr, "error", err)
	}

	s.publishEvent(bgCtx, SandboxStateEvent{
		Event: "sandbox.paused", SandboxID: sandboxIDStr, TeamID: teamIDStr, HostID: hostIDStr,
		Timestamp: time.Now().Unix(),
	})
}

// Resume asynchronously resumes a paused sandbox on its original host.
// The DB CAS from "paused" to "resuming" gates concurrent Resume/Destroy.
func (s *SandboxService) Resume(ctx context.Context, sandboxID, teamID pgtype.UUID) (db.Sandbox, error) {
	sb, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		return db.Sandbox{}, apperr.SandboxNotFound.Wrap(err)
	}
	if sb.Status == "running" {
		return sb, nil
	}

	if _, err := s.DB.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
		ID: sandboxID, Status: "paused", Status_2: "resuming",
	}); err != nil {
		return db.Sandbox{}, apperr.SandboxNotPaused.Wrap(err).With("status", sb.Status)
	}

	agent, err := s.agentForHost(ctx, sb.HostID)
	if err != nil {
		if _, rerr := s.DB.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: "resuming", Status_2: "paused",
		}); rerr != nil {
			slog.Warn("failed to roll back resuming→paused", "id", id.FormatSandboxID(sandboxID), "error", rerr)
		}
		return db.Sandbox{}, err
	}

	// Look up template defaults so a resumed sandbox has the same env as
	// the original Create did.
	var defaultUser string
	var defaultEnv map[string]string
	if tmpl, terr := s.DB.GetTemplate(ctx, sb.TemplateID); terr == nil {
		defaultUser = tmpl.DefaultUser
		if len(tmpl.DefaultEnv) > 0 {
			_ = json.Unmarshal(tmpl.DefaultEnv, &defaultEnv)
		}
	}

	sandboxIDStr := id.FormatSandboxID(sandboxID)
	hostIDStr := id.FormatHostID(sb.HostID)
	teamIDStr := id.FormatTeamID(sb.TeamID)

	go s.resumeInBackground(sandboxID, sandboxIDStr, hostIDStr, teamIDStr, agent, sb.TimeoutSec, defaultUser, defaultEnv)

	sb.Status = "resuming"
	return sb, nil
}

func (s *SandboxService) resumeInBackground(
	sandboxID pgtype.UUID, sandboxIDStr, hostIDStr, teamIDStr string,
	agent hostagentClient, timeoutSec int32,
	defaultUser string, defaultEnv map[string]string,
) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	resp, err := agent.ResumeSandbox(bgCtx, connect.NewRequest(&pb.ResumeSandboxRequest{
		SandboxId:   sandboxIDStr,
		TimeoutSec:  timeoutSec,
		DefaultUser: defaultUser,
		DefaultEnv:  defaultEnv,
	}))
	if err != nil {
		slog.Warn("background resume failed", "sandbox_id", sandboxIDStr, "error", err)
		if _, dbErr := s.DB.UpdateSandboxStatusIf(bgCtx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: "resuming", Status_2: "paused",
		}); dbErr != nil {
			slog.Warn("failed to recover resuming→paused after resume failure", "id", sandboxIDStr, "error", dbErr)
		}
		s.publishEvent(bgCtx, SandboxStateEvent{
			Event: "sandbox.resume_failed", SandboxID: sandboxIDStr, TeamID: teamIDStr, HostID: hostIDStr,
			Error: err.Error(), Timestamp: time.Now().Unix(),
		})
		return
	}

	now := time.Now()
	if _, err := s.DB.UpdateSandboxRunningIf(bgCtx, db.UpdateSandboxRunningIfParams{
		ID:        sandboxID,
		Status:    "resuming",
		HostIp:    resp.Msg.HostIp,
		StartedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}); err != nil {
		slog.Warn("failed to update sandbox to running after resume", "id", sandboxIDStr, "error", err)
	}

	// Refresh system metadata but preserve the user's labels — read the current
	// row and merge agent keys over it rather than overwriting wholesale.
	if len(resp.Msg.Metadata) > 0 {
		var existing map[string]string
		if cur, err := s.DB.GetSandbox(bgCtx, sandboxID); err == nil && len(cur.Metadata) > 0 {
			_ = json.Unmarshal(cur.Metadata, &existing)
		}
		s.persistSystemMetadata(bgCtx, sandboxID, sandboxIDStr, existing, resp.Msg.Metadata)
	}

	s.publishEvent(bgCtx, SandboxStateEvent{
		Event: "sandbox.resumed", SandboxID: sandboxIDStr, TeamID: teamIDStr, HostID: hostIDStr,
		HostIP: resp.Msg.HostIp, Metadata: resp.Msg.Metadata,
		Timestamp: now.Unix(),
	})
}

// CreateSnapshot asynchronously snapshots a running or paused sandbox,
// publishing the result as a new template owned by the sandbox's team. The DB
// CAS from the sandbox's current status to "snapshotting" is the authoritative
// gate against concurrent Pause/Snapshot/Destroy calls; if it loses, no agent
// RPC fires. A running sandbox is snapshotted live (CH briefly paused, then
// resumed); a paused sandbox is snapshotted from its on-disk artefacts without
// reviving the VM. Either way the sandbox returns to its original status on
// completion. Returns the sandbox (now "snapshotting") and the resolved name.
func (s *SandboxService) CreateSnapshot(ctx context.Context, sandboxID, teamID pgtype.UUID, name string) (db.Sandbox, string, error) {
	sb, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		return db.Sandbox{}, "", apperr.SandboxNotFound.Wrap(err)
	}
	if sb.Status != "running" && sb.Status != "paused" {
		return db.Sandbox{}, "", apperr.Conflict.Msg("Sandbox must be running or paused to snapshot.").With("status", sb.Status)
	}
	origStatus := sb.Status

	if name == "" {
		name = id.NewSnapshotName()
	}
	if err := validate.SafeName(name); err != nil {
		return db.Sandbox{}, "", apperr.ValidationFailed.Msgf("Invalid snapshot name: %v.", err)
	}
	// Reject duplicate names up front so we don't pause the VM and dump memory
	// only to fail on the template insert at the very end.
	if _, err := s.DB.GetTemplateByTeam(ctx, db.GetTemplateByTeamParams{Name: name, TeamID: teamID}); err == nil {
		return db.Sandbox{}, "", apperr.Conflict.Msgf("A snapshot named %q already exists.", name)
	}

	if _, err := s.DB.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
		ID: sandboxID, Status: origStatus, Status_2: "snapshotting",
	}); err != nil {
		return db.Sandbox{}, "", apperr.Conflict.WrapMsg(err, fmt.Sprintf("Sandbox is no longer in %s state.", origStatus)).With("status", origStatus)
	}

	agent, err := s.agentForHost(ctx, sb.HostID)
	if err != nil {
		// Roll back the CAS so the sandbox isn't stuck in "snapshotting".
		if _, rerr := s.DB.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: "snapshotting", Status_2: origStatus,
		}); rerr != nil {
			slog.Warn("failed to roll back snapshotting→"+origStatus, "id", id.FormatSandboxID(sandboxID), "error", rerr)
		}
		return db.Sandbox{}, "", err
	}

	sandboxIDStr := id.FormatSandboxID(sandboxID)
	hostIDStr := id.FormatHostID(sb.HostID)
	teamIDStr := id.FormatTeamID(sb.TeamID)

	// Notify other clients that the badge moved to "snapshotting".
	s.publishStateChanged(ctx, sandboxIDStr, teamIDStr, hostIDStr, origStatus, "snapshotting")

	go s.snapshotInBackground(sandboxID, sandboxIDStr, hostIDStr, teamIDStr, teamID, agent, name, origStatus, sb.Vcpus, sb.MemoryMb)

	sb.Status = "snapshotting"
	return sb, name, nil
}

func (s *SandboxService) snapshotInBackground(
	sandboxID pgtype.UUID, sandboxIDStr, hostIDStr, teamIDStr string, teamID pgtype.UUID,
	agent hostagentClient, name, origStatus string, vcpus, memoryMB int32,
) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	newTemplateID := id.NewSandboxID() // any random UUID
	templateUUID := pgtype.UUID{Bytes: newTemplateID.Bytes, Valid: true}

	resp, err := agent.CreateSnapshot(bgCtx, connect.NewRequest(&pb.CreateSnapshotRequest{
		SandboxId:  sandboxIDStr,
		Name:       name,
		TeamId:     id.UUIDString(teamID),
		TemplateId: id.UUIDString(templateUUID),
	}))

	// Either way, the host-side op is done; return the badge to its original
	// status (running for a live snapshot, paused for an on-disk one). Use a CAS
	// so a concurrent Destroy (which sets "stopping") wins: if the CAS misses,
	// the sandbox is no longer ours and we must NOT announce its old status. The
	// snapshot itself is still valid and is registered below — a snapshot
	// template outlives its source sandbox.
	if _, derr := s.DB.UpdateSandboxStatusIf(bgCtx, db.UpdateSandboxStatusIfParams{
		ID: sandboxID, Status: "snapshotting", Status_2: origStatus,
	}); derr != nil {
		slog.Warn("snapshotting→"+origStatus+" CAS missed (sandbox moved on); skipping state signal", "sandbox_id", sandboxIDStr, "error", derr)
	} else {
		s.publishStateChanged(bgCtx, sandboxIDStr, teamIDStr, hostIDStr, "snapshotting", origStatus)
	}

	if err != nil {
		slog.Warn("background snapshot failed", "sandbox_id", sandboxIDStr, "error", err)
		s.publishEvent(bgCtx, SandboxStateEvent{
			Event: "sandbox.snapshot_failed", SandboxID: sandboxIDStr, TeamID: teamIDStr, HostID: hostIDStr,
			Metadata: map[string]string{"name": name}, Error: err.Error(), Timestamp: time.Now().Unix(),
		})
		return
	}

	if _, err := s.DB.InsertTemplate(bgCtx, db.InsertTemplateParams{
		ID:          templateUUID,
		Name:        name,
		Type:        "snapshot",
		Vcpus:       vcpus,
		MemoryMb:    memoryMB,
		SizeBytes:   resp.Msg.SizeBytes,
		TeamID:      teamID,
		DefaultUser: "",
		DefaultEnv:  []byte("{}"),
		Metadata:    []byte("{}"),
	}); err != nil {
		slog.Warn("failed to insert snapshot template", "sandbox_id", sandboxIDStr, "name", name, "error", err)
		s.publishEvent(bgCtx, SandboxStateEvent{
			Event: "sandbox.snapshot_failed", SandboxID: sandboxIDStr, TeamID: teamIDStr, HostID: hostIDStr,
			Metadata: map[string]string{"name": name}, Error: "failed to register snapshot", Timestamp: time.Now().Unix(),
		})
		return
	}

	s.publishEvent(bgCtx, SandboxStateEvent{
		Event: "sandbox.snapshotted", SandboxID: sandboxIDStr, TeamID: teamIDStr, HostID: hostIDStr,
		Metadata: map[string]string{"name": name}, Timestamp: time.Now().Unix(),
	})
}

// publishStateChanged emits a transient capsule.state.changed event so the
// dashboard flips the status badge during a transition that has no terminal
// lifecycle verb of its own (e.g. the snapshotting round-trip).
func (s *SandboxService) publishStateChanged(ctx context.Context, sandboxIDStr, teamIDStr, hostIDStr, from, to string) {
	s.publishEvent(ctx, SandboxStateEvent{
		Event: "sandbox.state_changed", SandboxID: sandboxIDStr, TeamID: teamIDStr, HostID: hostIDStr,
		Metadata: map[string]string{"from": from, "to": to}, Timestamp: time.Now().Unix(),
	})
}

// Destroy stops a sandbox asynchronously. Pre-marks the DB status as
// "stopping" and fires the agent RPC in a background goroutine.
func (s *SandboxService) Destroy(ctx context.Context, sandboxID, teamID pgtype.UUID) error {
	sb, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		return apperr.SandboxNotFound.Wrap(err)
	}
	if sb.Status == "stopped" || sb.Status == "error" {
		return nil
	}

	agent, _, err := s.agentForSandbox(ctx, sandboxID)
	if err != nil {
		return err
	}

	sandboxIDStr := id.FormatSandboxID(sandboxID)
	hostIDStr := id.FormatHostID(sb.HostID)
	teamIDStr := id.FormatTeamID(sb.TeamID)
	prevStatus := sb.Status

	if _, err := s.DB.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
		ID: sandboxID, Status: "stopping",
	}); err != nil {
		return fmt.Errorf("pre-mark stopping: %w", err)
	}

	go s.destroyInBackground(sandboxID, sandboxIDStr, hostIDStr, teamIDStr, agent, prevStatus)

	return nil
}

func (s *SandboxService) destroyInBackground(sandboxID pgtype.UUID, sandboxIDStr, hostIDStr, teamIDStr string, agent hostagentClient, prevStatus string) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if prevStatus == "running" || prevStatus == "pausing" {
		s.flushAndPersistMetrics(bgCtx, agent, sandboxID, false)
	}

	if _, err := agent.DestroySandbox(bgCtx, connect.NewRequest(&pb.DestroySandboxRequest{
		SandboxId: sandboxIDStr,
	})); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
		slog.Warn("background destroy failed", "sandbox_id", sandboxIDStr, "error", err)
	}

	if prevStatus == "paused" {
		_ = s.DB.DeleteSandboxMetricPointsByTier(bgCtx, db.DeleteSandboxMetricPointsByTierParams{
			SandboxID: sandboxID, Tier: "10m",
		})
		_ = s.DB.DeleteSandboxMetricPointsByTier(bgCtx, db.DeleteSandboxMetricPointsByTierParams{
			SandboxID: sandboxID, Tier: "2h",
		})
	}

	if _, err := s.DB.UpdateSandboxStatusIf(bgCtx, db.UpdateSandboxStatusIfParams{
		ID: sandboxID, Status: "stopping", Status_2: "stopped",
	}); err != nil {
		slog.Warn("failed to update sandbox to stopped", "sandbox_id", sandboxIDStr, "error", err)
	}

	s.publishEvent(bgCtx, SandboxStateEvent{
		Event: "sandbox.stopped", SandboxID: sandboxIDStr, TeamID: teamIDStr, HostID: hostIDStr,
		Timestamp: time.Now().Unix(),
	})
}

// flushAndPersistMetrics calls FlushSandboxMetrics on the agent and stores
// the returned data to DB. If allTiers is true, all three tiers are saved;
// otherwise only the 24h tier (for post-destroy analytics).
func (s *SandboxService) flushAndPersistMetrics(ctx context.Context, agent hostagentClient, sandboxID pgtype.UUID, allTiers bool) {
	sandboxIDStr := id.FormatSandboxID(sandboxID)
	resp, err := agent.FlushSandboxMetrics(ctx, connect.NewRequest(&pb.FlushSandboxMetricsRequest{
		SandboxId: sandboxIDStr,
	}))
	if err != nil {
		slog.Warn("flush metrics failed (best-effort)", "sandbox_id", sandboxIDStr, "error", err)
		return
	}
	msg := resp.Msg

	if allTiers {
		s.persistMetricPoints(ctx, sandboxID, "10m", msg.Points_10M)
		s.persistMetricPoints(ctx, sandboxID, "2h", msg.Points_2H)
	}
	s.persistMetricPoints(ctx, sandboxID, "24h", msg.Points_24H)
}

func (s *SandboxService) persistMetricPoints(ctx context.Context, sandboxID pgtype.UUID, tier string, points []*pb.MetricPoint) {
	sandboxIDStr := id.FormatSandboxID(sandboxID)
	for _, p := range points {
		if err := s.DB.InsertSandboxMetricPoint(ctx, db.InsertSandboxMetricPointParams{
			SandboxID: sandboxID,
			Tier:      tier,
			Ts:        p.TimestampUnix,
			CpuPct:    p.CpuPct,
			MemBytes:  p.MemBytes,
			DiskBytes: p.DiskBytes,
		}); err != nil {
			slog.Warn("persist metric point failed", "sandbox_id", sandboxIDStr, "tier", tier, "error", err)
		}
	}
}

// GetDiskUsage returns the current disk usage in bytes for a sandbox.
// For running or paused sandboxes, it queries the host agent for live data.
// For other states or when the agent is unreachable, it falls back to the
// last known metric point from the database.
func (s *SandboxService) GetDiskUsage(ctx context.Context, sandboxID, teamID pgtype.UUID) (int64, error) {
	sb, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		return 0, apperr.SandboxNotFound.Wrap(err)
	}

	// For running or paused sandboxes, try the agent for live disk usage.
	if sb.Status == "running" || sb.Status == "paused" {
		sandboxIDStr := id.FormatSandboxID(sandboxID)
		agent, hostErr := s.agentForHost(ctx, sb.HostID)
		if hostErr == nil {
			resp, err := agent.GetSandboxMetrics(ctx, connect.NewRequest(&pb.GetSandboxMetricsRequest{
				SandboxId: sandboxIDStr,
				Range:     "5m",
			}))
			if err == nil && len(resp.Msg.Points) > 0 {
				last := resp.Msg.Points[len(resp.Msg.Points)-1]
				return last.DiskBytes, nil
			}
		}
	}

	// Fallback: query the database for the last known metric point.
	point, err := s.DB.GetLatestSandboxMetricPoint(ctx, sandboxID)
	if err != nil {
		return 0, err
	}
	return point.DiskBytes, nil
}

// Ping resets the inactivity timer for a running sandbox.
func (s *SandboxService) Ping(ctx context.Context, sandboxID, teamID pgtype.UUID) error {
	sb, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		return apperr.SandboxNotFound.Wrap(err)
	}
	if sb.Status != "running" {
		return apperr.SandboxNotRunning.New().With("status", sb.Status)
	}

	agent, _, err := s.agentForSandbox(ctx, sandboxID)
	if err != nil {
		return err
	}

	sandboxIDStr := id.FormatSandboxID(sandboxID)

	if _, err := agent.PingSandbox(ctx, connect.NewRequest(&pb.PingSandboxRequest{
		SandboxId: sandboxIDStr,
	})); err != nil {
		return fmt.Errorf("agent ping: %w", err)
	}

	if err := s.DB.UpdateLastActive(ctx, db.UpdateLastActiveParams{
		ID: sandboxID,
		LastActiveAt: pgtype.Timestamptz{
			Time:  time.Now(),
			Valid: true,
		},
	}); err != nil {
		slog.Warn("ping: failed to update last_active_at", "sandbox_id", sandboxIDStr, "error", err)
	}
	return nil
}
