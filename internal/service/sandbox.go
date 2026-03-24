package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	"git.omukk.dev/wrenn/sandbox/internal/scheduler"
	"git.omukk.dev/wrenn/sandbox/internal/validate"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
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
	TeamID     string
	Template   string
	VCPUs      int32
	MemoryMB   int32
	TimeoutSec int32
}

// agentForSandbox looks up the host for the given sandbox and returns a client.
func (s *SandboxService) agentForSandbox(ctx context.Context, sandboxID string) (hostagentClient, db.Sandbox, error) {
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

	// If the template is a snapshot, use its baked-in vcpus/memory.
	if tmpl, err := s.DB.GetTemplateByTeam(ctx, db.GetTemplateByTeamParams{Name: p.Template, TeamID: p.TeamID}); err == nil && tmpl.Type == "snapshot" {
		if tmpl.Vcpus.Valid {
			p.VCPUs = tmpl.Vcpus.Int32
		}
		if tmpl.MemoryMb.Valid {
			p.MemoryMB = tmpl.MemoryMb.Int32
		}
	}

	// Pick a host for this sandbox.
	host, err := s.Scheduler.SelectHost(ctx)
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("select host: %w", err)
	}

	agent, err := s.Pool.GetForHost(host)
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("get agent client: %w", err)
	}

	sandboxID := id.NewSandboxID()

	if _, err := s.DB.InsertSandbox(ctx, db.InsertSandboxParams{
		ID:         sandboxID,
		TeamID:     p.TeamID,
		HostID:     host.ID,
		Template:   p.Template,
		Status:     "pending",
		Vcpus:      p.VCPUs,
		MemoryMb:   p.MemoryMB,
		TimeoutSec: p.TimeoutSec,
	}); err != nil {
		return db.Sandbox{}, fmt.Errorf("insert sandbox: %w", err)
	}

	resp, err := agent.CreateSandbox(ctx, connect.NewRequest(&pb.CreateSandboxRequest{
		SandboxId:  sandboxID,
		Template:   p.Template,
		Vcpus:      p.VCPUs,
		MemoryMb:   p.MemoryMB,
		TimeoutSec: p.TimeoutSec,
	}))
	if err != nil {
		if _, dbErr := s.DB.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
			ID: sandboxID, Status: "error",
		}); dbErr != nil {
			slog.Warn("failed to update sandbox status to error", "id", sandboxID, "error", dbErr)
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

	return sb, nil
}

// List returns active sandboxes (excludes stopped/error) belonging to the given team.
func (s *SandboxService) List(ctx context.Context, teamID string) ([]db.Sandbox, error) {
	return s.DB.ListSandboxesByTeam(ctx, teamID)
}

// Get returns a single sandbox by ID, scoped to the given team.
func (s *SandboxService) Get(ctx context.Context, sandboxID, teamID string) (db.Sandbox, error) {
	return s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID})
}

// Pause snapshots and freezes a running sandbox to disk.
func (s *SandboxService) Pause(ctx context.Context, sandboxID, teamID string) (db.Sandbox, error) {
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

	if _, err := agent.PauseSandbox(ctx, connect.NewRequest(&pb.PauseSandboxRequest{
		SandboxId: sandboxID,
	})); err != nil {
		return db.Sandbox{}, fmt.Errorf("agent pause: %w", err)
	}

	sb, err = s.DB.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
		ID: sandboxID, Status: "paused",
	})
	if err != nil {
		return db.Sandbox{}, fmt.Errorf("update status: %w", err)
	}
	return sb, nil
}

// Resume restores a paused sandbox from snapshot.
func (s *SandboxService) Resume(ctx context.Context, sandboxID, teamID string) (db.Sandbox, error) {
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

	resp, err := agent.ResumeSandbox(ctx, connect.NewRequest(&pb.ResumeSandboxRequest{
		SandboxId:  sandboxID,
		TimeoutSec: sb.TimeoutSec,
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
	return sb, nil
}

// Destroy stops a sandbox and marks it as stopped.
func (s *SandboxService) Destroy(ctx context.Context, sandboxID, teamID string) error {
	if _, err := s.DB.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: teamID}); err != nil {
		return fmt.Errorf("sandbox not found: %w", err)
	}

	agent, _, err := s.agentForSandbox(ctx, sandboxID)
	if err != nil {
		return err
	}

	// Destroy on host agent. A not-found response is fine — sandbox is already gone.
	if _, err := agent.DestroySandbox(ctx, connect.NewRequest(&pb.DestroySandboxRequest{
		SandboxId: sandboxID,
	})); err != nil && connect.CodeOf(err) != connect.CodeNotFound {
		return fmt.Errorf("agent destroy: %w", err)
	}

	if _, err := s.DB.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
		ID: sandboxID, Status: "stopped",
	}); err != nil {
		return fmt.Errorf("update status: %w", err)
	}
	return nil
}

// Ping resets the inactivity timer for a running sandbox.
func (s *SandboxService) Ping(ctx context.Context, sandboxID, teamID string) error {
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

	if _, err := agent.PingSandbox(ctx, connect.NewRequest(&pb.PingSandboxRequest{
		SandboxId: sandboxID,
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
		slog.Warn("ping: failed to update last_active_at", "sandbox_id", sandboxID, "error", err)
	}
	return nil
}
