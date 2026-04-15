package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/scheduler"
	"git.omukk.dev/wrenn/wrenn/pkg/validate"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

// SandboxService provides sandbox lifecycle operations shared between the
// REST API and the dashboard.
type SandboxService struct {
	DB        *db.Queries
	Pool      *lifecycle.HostClientPool
	Scheduler scheduler.HostScheduler
}

// SandboxCreateParams holds the parameters for creating a sandbox.
type SandboxCreateParams struct {
	TeamID     pgtype.UUID
	Template   string
	VCPUs      int32
	MemoryMB   int32
	TimeoutSec int32
	DiskSizeMB int32
}

// agentForSandbox looks up the host for the given sandbox and returns a client.
func (s *SandboxService) agentForSandbox(ctx context.Context, sandboxID pgtype.UUID) (hostagentClient, db.Sandbox, error) {
	sb, err := s.DB.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, db.Sandbox{}, fmt.Errorf("sandbox not found: %w", err)
	}
	host, err := s.DB.GetHost(ctx, sb.HostID)
	if err != nil {
		return nil, db.Sandbox{}, fmt.Errorf("host not found for sandbox: %w", err)
	}
	agent, err := s.Pool.GetForHost(host)
	if err != nil {
		return nil, db.Sandbox{}, fmt.Errorf("get agent client: %w", err)
	}
	return agent, sb, nil
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
}

// Create creates a new sandbox: picks a host via the scheduler, inserts a pending
// DB record, calls the host agent, and updates the record to running.
func (s *SandboxService) Create(ctx context.Context, p SandboxCreateParams) (db.Sandbox, error) {
	if p.Template == "" {
		p.Template = "minimal"
	}
	if err := validate.SafeName(p.Template); err != nil {
		return db.Sandbox{}, fmt.Errorf("invalid template name: %w", err)
	}
	if p.VCPUs <= 0 {
		p.VCPUs = 1
	}
	if p.MemoryMB <= 0 {
		p.MemoryMB = 512
	}
	if p.DiskSizeMB <= 0 {
		p.DiskSizeMB = 5120 // 5 GB default
	}

	// Resolve template name → (teamID, templateID).
	templateTeamID := id.PlatformTeamID
	templateID := id.MinimalTemplateID
	var templateDefaultUser string
	var templateDefaultEnv map[string]string
	if p.Template != "minimal" {
		tmpl, err := s.DB.GetTemplateByTeam(ctx, db.GetTemplateByTeamParams{Name: p.Template, TeamID: p.TeamID})
		if err != nil {
			return db.Sandbox{}, fmt.Errorf("template %q not found: %w", p.Template, err)
		}
		templateTeamID = tmpl.TeamID
		templateID = tmpl.ID
		templateDefaultUser = tmpl.DefaultUser
		// Parse default_env JSONB into a map.
		if len(tmpl.DefaultEnv) > 0 {
			_ = json.Unmarshal(tmpl.DefaultEnv, &templateDefaultEnv)
		}
		// If the template is a snapshot, use its baked-in vcpus/memory.
		if tmpl.Type == "snapshot" {
			p.VCPUs = tmpl.Vcpus
			p.MemoryMB = tmpl.MemoryMb
		}
	}

	if !p.TeamID.Valid {
		return db.Sandbox{}, fmt.Errorf("invalid request: team_id is required")
	}

	// Determine whether this team uses BYOC hosts or platform hosts.
	team, err := s.DB.GetTeam(ctx, p.TeamID)
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("team not found: %w", err)
	}

	// Pick a host for this sandbox.
	host, err := s.Scheduler.SelectHost(ctx, p.TeamID, team.IsByoc, p.MemoryMB, p.DiskSizeMB)
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("select host: %w", err)
	}

	agent, err := s.Pool.GetForHost(host)
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("get agent client: %w", err)
	}

	sandboxID := id.NewSandboxID()
	sandboxIDStr := id.FormatSandboxID(sandboxID)

	if _, err := s.DB.InsertSandbox(ctx, db.InsertSandboxParams{
		ID:             sandboxID,
		TeamID:         p.TeamID,
		HostID:         host.ID,
		Template:       p.Template,
		Status:         "pending",
		Vcpus:          p.VCPUs,
		MemoryMb:       p.MemoryMB,
		TimeoutSec:     p.TimeoutSec,
		DiskSizeMb:     p.DiskSizeMB,
		TemplateID:     templateID,
		TemplateTeamID: templateTeamID,
		Metadata:       []byte("{}"),
	}); err != nil {
		return db.Sandbox{}, fmt.Errorf("insert sandbox: %w", err)
	}

	resp, err := agent.CreateSandbox(ctx, connect.NewRequest(&pb.CreateSandboxRequest{
		SandboxId:   sandboxIDStr,
		Template:    p.Template,
		TeamId:      id.UUIDString(templateTeamID),
		TemplateId:  id.UUIDString(templateID),
		Vcpus:       p.VCPUs,
		MemoryMb:    p.MemoryMB,
		TimeoutSec:  p.TimeoutSec,
		DiskSizeMb:  p.DiskSizeMB,
		DefaultUser: templateDefaultUser,
		DefaultEnv:  templateDefaultEnv,
	}))
	if err != nil {
		if _, dbErr := s.DB.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
			ID: sandboxID, Status: "error",
		}); dbErr != nil {
			slog.Warn("failed to update sandbox status to error", "id", sandboxIDStr, "error", dbErr)
		}
		return db.Sandbox{}, fmt.Errorf("agent create: %w", err)
	}

	now := time.Now()
	sb, err := s.DB.UpdateSandboxRunning(ctx, db.UpdateSandboxRunningParams{
		ID:      sandboxID,
		HostIp:  resp.Msg.HostIp,
		GuestIp: "",
		StartedAt: pgtype.Timestamptz{
			Time:  now,
			Valid: true,
		},
	})
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("update sandbox running: %w", err)
	}

	// Store runtime metadata from the agent (envd/kernel/firecracker/agent versions).
	if meta := resp.Msg.Metadata; len(meta) > 0 {
		metaJSON, _ := json.Marshal(meta)
		if err := s.DB.UpdateSandboxMetadata(ctx, db.UpdateSandboxMetadataParams{
			ID:       sandboxID,
			Metadata: metaJSON,
		}); err != nil {
			slog.Warn("failed to store sandbox metadata", "id", sandboxIDStr, "error", err)
		}
		sb.Metadata = metaJSON
	}

	return sb, nil
}

// List returns active sandboxes (excludes stopped/error) belonging to the given team.
func (s *SandboxService) List(ctx context.Context, teamID pgtype.UUID) ([]db.Sandbox, error) {
	return s.DB.ListSandboxesByTeam(ctx, teamID)
}

// Get returns a single sandbox by ID, scoped to the given team.
func (s *SandboxService) Get(ctx context.Context, sandboxID, teamID pgtype.UUID) (db.Sandbox, error) {
	return s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
}

// Pause snapshots and freezes a running sandbox to disk.
func (s *SandboxService) Pause(ctx context.Context, sandboxID, teamID pgtype.UUID) (db.Sandbox, error) {
	sb, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("sandbox not found: %w", err)
	}
	if sb.Status != "running" {
		return db.Sandbox{}, fmt.Errorf("sandbox is not running (status: %s)", sb.Status)
	}

	agent, _, err := s.agentForSandbox(ctx, sandboxID)
	if err != nil {
		return db.Sandbox{}, err
	}

	sandboxIDStr := id.FormatSandboxID(sandboxID)

	// Pre-mark as "paused" in DB before the RPC so the reconciler does not
	// mark the sandbox "stopped" while the host agent processes the pause.
	if _, err := s.DB.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
		ID: sandboxID, Status: "paused",
	}); err != nil {
		return db.Sandbox{}, fmt.Errorf("pre-mark paused: %w", err)
	}

	// Flush all metrics tiers before pausing so data survives in DB.
	s.flushAndPersistMetrics(ctx, agent, sandboxID, true)

	if _, err := agent.PauseSandbox(ctx, connect.NewRequest(&pb.PauseSandboxRequest{
		SandboxId: sandboxIDStr,
	})); err != nil {
		// Revert status on failure.
		if _, dbErr := s.DB.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
			ID: sandboxID, Status: "running",
		}); dbErr != nil {
			slog.Warn("failed to revert sandbox status after pause error", "sandbox_id", sandboxIDStr, "error", dbErr)
		}
		return db.Sandbox{}, fmt.Errorf("agent pause: %w", err)
	}

	sb, err = s.DB.GetSandbox(ctx, sandboxID)
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("get sandbox after pause: %w", err)
	}
	return sb, nil
}

// Resume restores a paused sandbox from snapshot.
func (s *SandboxService) Resume(ctx context.Context, sandboxID, teamID pgtype.UUID) (db.Sandbox, error) {
	sb, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("sandbox not found: %w", err)
	}
	if sb.Status != "paused" {
		return db.Sandbox{}, fmt.Errorf("sandbox is not paused (status: %s)", sb.Status)
	}

	agent, _, err := s.agentForSandbox(ctx, sandboxID)
	if err != nil {
		return db.Sandbox{}, err
	}

	sandboxIDStr := id.FormatSandboxID(sandboxID)

	// Look up template defaults for resume.
	var resumeDefaultUser string
	var resumeDefaultEnv map[string]string
	if sb.TemplateID.Valid {
		tmpl, err := s.DB.GetTemplate(ctx, sb.TemplateID)
		if err == nil {
			resumeDefaultUser = tmpl.DefaultUser
			if len(tmpl.DefaultEnv) > 0 {
				_ = json.Unmarshal(tmpl.DefaultEnv, &resumeDefaultEnv)
			}
		}
	}

	// Extract kernel version hint from existing sandbox metadata.
	var kernelVersion string
	if len(sb.Metadata) > 0 {
		var meta map[string]string
		if err := json.Unmarshal(sb.Metadata, &meta); err == nil {
			kernelVersion = meta["kernel_version"]
		}
	}

	resp, err := agent.ResumeSandbox(ctx, connect.NewRequest(&pb.ResumeSandboxRequest{
		SandboxId:     sandboxIDStr,
		TimeoutSec:    sb.TimeoutSec,
		DefaultUser:   resumeDefaultUser,
		DefaultEnv:    resumeDefaultEnv,
		KernelVersion: kernelVersion,
	}))
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("agent resume: %w", err)
	}

	now := time.Now()
	sb, err = s.DB.UpdateSandboxRunning(ctx, db.UpdateSandboxRunningParams{
		ID:      sandboxID,
		HostIp:  resp.Msg.HostIp,
		GuestIp: "",
		StartedAt: pgtype.Timestamptz{
			Time:  now,
			Valid: true,
		},
	})
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("update status: %w", err)
	}

	// Update metadata with actual versions used after resume.
	if meta := resp.Msg.Metadata; len(meta) > 0 {
		metaJSON, _ := json.Marshal(meta)
		if err := s.DB.UpdateSandboxMetadata(ctx, db.UpdateSandboxMetadataParams{
			ID:       sandboxID,
			Metadata: metaJSON,
		}); err != nil {
			slog.Warn("failed to update sandbox metadata after resume", "id", sandboxIDStr, "error", err)
		}
		sb.Metadata = metaJSON
	}

	return sb, nil
}

// Destroy stops a sandbox and marks it as stopped.
func (s *SandboxService) Destroy(ctx context.Context, sandboxID, teamID pgtype.UUID) error {
	sb, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		return fmt.Errorf("sandbox not found: %w", err)
	}

	agent, _, err := s.agentForSandbox(ctx, sandboxID)
	if err != nil {
		return err
	}

	sandboxIDStr := id.FormatSandboxID(sandboxID)

	// If running, flush 24h tier metrics for analytics before destroying.
	if sb.Status == "running" {
		s.flushAndPersistMetrics(ctx, agent, sandboxID, false)
	}

	// Destroy on host agent. A not-found response is fine — sandbox is already gone.
	if _, err := agent.DestroySandbox(ctx, connect.NewRequest(&pb.DestroySandboxRequest{
		SandboxId: sandboxIDStr,
	})); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
		return fmt.Errorf("agent destroy: %w", err)
	}

	// For a paused sandbox, only keep 24h tier; remove the finer-grained tiers.
	if sb.Status == "paused" {
		_ = s.DB.DeleteSandboxMetricPointsByTier(ctx, db.DeleteSandboxMetricPointsByTierParams{
			SandboxID: sandboxID, Tier: "10m",
		})
		_ = s.DB.DeleteSandboxMetricPointsByTier(ctx, db.DeleteSandboxMetricPointsByTierParams{
			SandboxID: sandboxID, Tier: "2h",
		})
	}

	if _, err := s.DB.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
		ID: sandboxID, Status: "stopped",
	}); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
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

// Ping resets the inactivity timer for a running sandbox.
func (s *SandboxService) Ping(ctx context.Context, sandboxID, teamID pgtype.UUID) error {
	sb, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		return fmt.Errorf("sandbox not found: %w", err)
	}
	if sb.Status != "running" {
		return fmt.Errorf("sandbox is not running (status: %s)", sb.Status)
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
