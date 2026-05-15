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

// SandboxEventPublisher writes sandbox lifecycle events to the Redis stream.
type SandboxEventPublisher func(ctx context.Context, event SandboxStateEvent)

// SandboxStateEvent is the event payload published to the Redis stream.
type SandboxStateEvent struct {
	Event     string            `json:"event"`
	SandboxID string            `json:"sandbox_id"`
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
}

// Create creates a new sandbox asynchronously: picks a host, inserts a
// "starting" DB record, fires the agent RPC in a background goroutine, and
// returns the sandbox immediately. The background goroutine publishes a
// sandbox event to the Redis stream when the operation completes.
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
		if len(tmpl.DefaultEnv) > 0 {
			_ = json.Unmarshal(tmpl.DefaultEnv, &templateDefaultEnv)
		}
		if tmpl.Type == "snapshot" {
			p.VCPUs = tmpl.Vcpus
			p.MemoryMB = tmpl.MemoryMb
		}
	}

	if !p.TeamID.Valid {
		return db.Sandbox{}, fmt.Errorf("invalid request: team_id is required")
	}

	team, err := s.DB.GetTeam(ctx, p.TeamID)
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("team not found: %w", err)
	}

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
	hostIDStr := id.FormatHostID(host.ID)

	sb, err := s.DB.InsertSandbox(ctx, db.InsertSandboxParams{
		ID:             sandboxID,
		TeamID:         p.TeamID,
		HostID:         host.ID,
		Template:       p.Template,
		Status:         "starting",
		Vcpus:          p.VCPUs,
		MemoryMb:       p.MemoryMB,
		TimeoutSec:     p.TimeoutSec,
		DiskSizeMb:     p.DiskSizeMB,
		TemplateID:     templateID,
		TemplateTeamID: templateTeamID,
		Metadata:       []byte("{}"),
	})
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("insert sandbox: %w", err)
	}

	go s.createInBackground(sandboxID, sandboxIDStr, hostIDStr, agent, p, templateTeamID, templateID, templateDefaultUser, templateDefaultEnv)

	return sb, nil
}

func (s *SandboxService) createInBackground(
	sandboxID pgtype.UUID, sandboxIDStr, hostIDStr string,
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
		DiskSizeMb:  p.DiskSizeMB,
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
			Event: "sandbox.failed", SandboxID: sandboxIDStr, HostID: hostIDStr,
			Error: err.Error(), Timestamp: time.Now().Unix(),
		})
		return
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

	if meta := resp.Msg.Metadata; len(meta) > 0 {
		metaJSON, _ := json.Marshal(meta)
		if err := s.DB.UpdateSandboxMetadata(bgCtx, db.UpdateSandboxMetadataParams{
			ID: sandboxID, Metadata: metaJSON,
		}); err != nil {
			slog.Warn("failed to store sandbox metadata", "id", sandboxIDStr, "error", err)
		}
	}

	s.publishEvent(bgCtx, SandboxStateEvent{
		Event: "sandbox.started", SandboxID: sandboxIDStr, HostID: hostIDStr,
		HostIP: resp.Msg.HostIp, Metadata: resp.Msg.Metadata,
		Timestamp: now.Unix(),
	})
}

// List returns active sandboxes (excludes stopped/error) belonging to the given team.
func (s *SandboxService) List(ctx context.Context, teamID pgtype.UUID) ([]db.Sandbox, error) {
	return s.DB.ListSandboxesByTeam(ctx, teamID)
}

// Get returns a single sandbox by ID, scoped to the given team.
func (s *SandboxService) Get(ctx context.Context, sandboxID, teamID pgtype.UUID) (db.Sandbox, error) {
	return s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
}

// Pause snapshots and freezes a running sandbox to disk asynchronously.
// Pre-marks the DB status as "pausing" and fires the agent RPC in a
// background goroutine.
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
	hostIDStr := id.FormatHostID(sb.HostID)

	sb, err = s.DB.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
		ID: sandboxID, Status: "running", Status_2: "pausing",
	})
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("sandbox status changed concurrently")
	}

	go s.pauseInBackground(sandboxID, sandboxIDStr, hostIDStr, agent)

	return sb, nil
}

func (s *SandboxService) pauseInBackground(sandboxID pgtype.UUID, sandboxIDStr, hostIDStr string, agent hostagentClient) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	s.flushAndPersistMetrics(bgCtx, agent, sandboxID, true)

	if _, err := agent.PauseSandbox(bgCtx, connect.NewRequest(&pb.PauseSandboxRequest{
		SandboxId: sandboxIDStr,
	})); err != nil {
		revertStatus := "running"
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if _, pingErr := agent.PingSandbox(pingCtx, connect.NewRequest(&pb.PingSandboxRequest{
			SandboxId: sandboxIDStr,
		})); pingErr != nil {
			revertStatus = "error"
			slog.Warn("sandbox gone from agent after failed pause, marking as error", "sandbox_id", sandboxIDStr)
		}
		pingCancel()
		dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, dbErr := s.DB.UpdateSandboxStatusIf(dbCtx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: "pausing", Status_2: revertStatus,
		}); dbErr != nil {
			slog.Warn("failed to revert sandbox status after pause error", "sandbox_id", sandboxIDStr, "error", dbErr)
		}
		dbCancel()

		evtCtx, evtCancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.publishEvent(evtCtx, SandboxStateEvent{
			Event: "sandbox.failed", SandboxID: sandboxIDStr, HostID: hostIDStr,
			Error: err.Error(), Timestamp: time.Now().Unix(),
		})
		evtCancel()
		return
	}

	if _, err := s.DB.UpdateSandboxStatusIf(bgCtx, db.UpdateSandboxStatusIfParams{
		ID: sandboxID, Status: "pausing", Status_2: "paused",
	}); err != nil {
		slog.Warn("failed to update sandbox to paused", "sandbox_id", sandboxIDStr, "error", err)
	}

	s.publishEvent(bgCtx, SandboxStateEvent{
		Event: "sandbox.paused", SandboxID: sandboxIDStr, HostID: hostIDStr,
		Timestamp: time.Now().Unix(),
	})
}

// Resume restores a paused sandbox from snapshot asynchronously.
// Pre-marks the DB status as "resuming" and fires the agent RPC in a
// background goroutine.
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
	hostIDStr := id.FormatHostID(sb.HostID)

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

	var kernelVersion string
	if len(sb.Metadata) > 0 {
		var meta map[string]string
		if err := json.Unmarshal(sb.Metadata, &meta); err == nil {
			kernelVersion = meta["kernel_version"]
		}
	}

	sb, err = s.DB.UpdateSandboxStatusIf(ctx, db.UpdateSandboxStatusIfParams{
		ID: sandboxID, Status: "paused", Status_2: "resuming",
	})
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("sandbox status changed concurrently")
	}

	go s.resumeInBackground(sandboxID, sandboxIDStr, hostIDStr, agent, sb.TimeoutSec, resumeDefaultUser, resumeDefaultEnv, kernelVersion)

	return sb, nil
}

func (s *SandboxService) resumeInBackground(
	sandboxID pgtype.UUID, sandboxIDStr, hostIDStr string,
	agent hostagentClient, timeoutSec int32,
	defaultUser string, defaultEnv map[string]string, kernelVersion string,
) {
	bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	resp, err := agent.ResumeSandbox(bgCtx, connect.NewRequest(&pb.ResumeSandboxRequest{
		SandboxId:     sandboxIDStr,
		TimeoutSec:    timeoutSec,
		DefaultUser:   defaultUser,
		DefaultEnv:    defaultEnv,
		KernelVersion: kernelVersion,
	}))
	if err != nil {
		slog.Warn("background resume failed", "sandbox_id", sandboxIDStr, "error", err)
		errCtx, errCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer errCancel()
		if _, dbErr := s.DB.UpdateSandboxStatusIf(errCtx, db.UpdateSandboxStatusIfParams{
			ID: sandboxID, Status: "resuming", Status_2: "paused",
		}); dbErr != nil {
			slog.Warn("failed to revert sandbox to paused after resume failure", "id", sandboxIDStr, "error", dbErr)
		}
		s.publishEvent(errCtx, SandboxStateEvent{
			Event: "sandbox.failed", SandboxID: sandboxIDStr, HostID: hostIDStr,
			Error: err.Error(), Timestamp: time.Now().Unix(),
		})
		return
	}

	now := time.Now()
	if _, dbErr := s.DB.UpdateSandboxRunningIf(bgCtx, db.UpdateSandboxRunningIfParams{
		ID:     sandboxID,
		Status: "resuming",
		HostIp: resp.Msg.HostIp,
		StartedAt: pgtype.Timestamptz{
			Time:  now,
			Valid: true,
		},
	}); dbErr != nil {
		slog.Warn("failed to update sandbox running after resume", "id", sandboxIDStr, "error", dbErr)
	}

	if meta := resp.Msg.Metadata; len(meta) > 0 {
		metaJSON, _ := json.Marshal(meta)
		if err := s.DB.UpdateSandboxMetadata(bgCtx, db.UpdateSandboxMetadataParams{
			ID: sandboxID, Metadata: metaJSON,
		}); err != nil {
			slog.Warn("failed to update sandbox metadata after resume", "id", sandboxIDStr, "error", err)
		}
	}

	s.publishEvent(bgCtx, SandboxStateEvent{
		Event: "sandbox.resumed", SandboxID: sandboxIDStr, HostID: hostIDStr,
		HostIP: resp.Msg.HostIp, Metadata: resp.Msg.Metadata,
		Timestamp: now.Unix(),
	})
}

// Destroy stops a sandbox asynchronously. Pre-marks the DB status as
// "stopping" and fires the agent RPC in a background goroutine.
func (s *SandboxService) Destroy(ctx context.Context, sandboxID, teamID pgtype.UUID) error {
	sb, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
	if err != nil {
		return fmt.Errorf("sandbox not found: %w", err)
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
	prevStatus := sb.Status

	if _, err := s.DB.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
		ID: sandboxID, Status: "stopping",
	}); err != nil {
		return fmt.Errorf("pre-mark stopping: %w", err)
	}

	go s.destroyInBackground(sandboxID, sandboxIDStr, hostIDStr, agent, prevStatus)

	return nil
}

func (s *SandboxService) destroyInBackground(sandboxID pgtype.UUID, sandboxIDStr, hostIDStr string, agent hostagentClient, prevStatus string) {
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
		Event: "sandbox.stopped", SandboxID: sandboxIDStr, HostID: hostIDStr,
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
