package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	"git.omukk.dev/wrenn/sandbox/internal/scheduler"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
)

const (
	buildQueueKey       = "wrenn:build_queue"
	buildCommandTimeout = 30 * time.Second
	healthcheckInterval = 1 * time.Second
	healthcheckTimeout  = 60 * time.Second
)

// preBuildCmds run before the user recipe to prepare the build environment.
var preBuildCmds = []string{
	"apt update",
}

// postBuildCmds run after the user recipe to clean up caches and reduce image size.
var postBuildCmds = []string{
	"apt clean",
	"apt autoremove -y",
	"rm -rf /var/lib/apt/lists/*",
}

// buildAgentClient is the subset of the host agent client used by the build worker.
type buildAgentClient interface {
	CreateSandbox(ctx context.Context, req *connect.Request[pb.CreateSandboxRequest]) (*connect.Response[pb.CreateSandboxResponse], error)
	DestroySandbox(ctx context.Context, req *connect.Request[pb.DestroySandboxRequest]) (*connect.Response[pb.DestroySandboxResponse], error)
	Exec(ctx context.Context, req *connect.Request[pb.ExecRequest]) (*connect.Response[pb.ExecResponse], error)
	CreateSnapshot(ctx context.Context, req *connect.Request[pb.CreateSnapshotRequest]) (*connect.Response[pb.CreateSnapshotResponse], error)
	FlattenRootfs(ctx context.Context, req *connect.Request[pb.FlattenRootfsRequest]) (*connect.Response[pb.FlattenRootfsResponse], error)
}

// BuildLogEntry represents a single entry in the build log JSONB array.
type BuildLogEntry struct {
	Step    int    `json:"step"`
	Phase   string `json:"phase"` // "pre-build", "recipe", or "post-build"
	Cmd     string `json:"cmd"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Exit    int32  `json:"exit"`
	Ok      bool   `json:"ok"`
	Elapsed int64  `json:"elapsed_ms"`
}

// BuildService handles template build orchestration.
type BuildService struct {
	DB        *db.Queries
	Redis     *redis.Client
	Pool      *lifecycle.HostClientPool
	Scheduler scheduler.HostScheduler
}

// BuildCreateParams holds the parameters for creating a template build.
type BuildCreateParams struct {
	Name         string
	BaseTemplate string
	Recipe       []string
	Healthcheck  string
	VCPUs        int32
	MemoryMB     int32
}

// Create inserts a new build record and enqueues it to Redis.
func (s *BuildService) Create(ctx context.Context, p BuildCreateParams) (db.TemplateBuild, error) {
	if p.BaseTemplate == "" {
		p.BaseTemplate = "minimal"
	}
	if p.VCPUs <= 0 {
		p.VCPUs = 1
	}
	if p.MemoryMB <= 0 {
		p.MemoryMB = 512
	}

	recipeJSON, err := json.Marshal(p.Recipe)
	if err != nil {
		return db.TemplateBuild{}, fmt.Errorf("marshal recipe: %w", err)
	}

	buildID := id.NewBuildID()
	buildIDStr := id.FormatBuildID(buildID)

	build, err := s.DB.InsertTemplateBuild(ctx, db.InsertTemplateBuildParams{
		ID:           buildID,
		Name:         p.Name,
		BaseTemplate: p.BaseTemplate,
		Recipe:       recipeJSON,
		Healthcheck:  p.Healthcheck,
		Vcpus:        p.VCPUs,
		MemoryMb:     p.MemoryMB,
		TotalSteps:   int32(len(p.Recipe) + len(preBuildCmds) + len(postBuildCmds)),
	})
	if err != nil {
		return db.TemplateBuild{}, fmt.Errorf("insert build: %w", err)
	}

	// Enqueue build ID (as formatted string) to Redis for workers to pick up.
	if err := s.Redis.RPush(ctx, buildQueueKey, buildIDStr).Err(); err != nil {
		return db.TemplateBuild{}, fmt.Errorf("enqueue build: %w", err)
	}

	return build, nil
}

// Get returns a single build by ID.
func (s *BuildService) Get(ctx context.Context, buildID pgtype.UUID) (db.TemplateBuild, error) {
	return s.DB.GetTemplateBuild(ctx, buildID)
}

// List returns all builds ordered by creation time.
func (s *BuildService) List(ctx context.Context) ([]db.TemplateBuild, error) {
	return s.DB.ListTemplateBuilds(ctx)
}

// StartWorkers launches n goroutines that consume from the Redis build queue.
// The returned cancel function stops all workers.
func (s *BuildService) StartWorkers(ctx context.Context, n int) context.CancelFunc {
	ctx, cancel := context.WithCancel(ctx)
	for i := range n {
		go s.worker(ctx, i)
	}
	slog.Info("build workers started", "count", n)
	return cancel
}

func (s *BuildService) worker(ctx context.Context, workerID int) {
	log := slog.With("worker", workerID)
	for {
		// BLPOP blocks until a build ID is available or context is cancelled.
		result, err := s.Redis.BLPop(ctx, 0, buildQueueKey).Result()
		if err != nil {
			if ctx.Err() != nil {
				log.Info("build worker shutting down")
				return
			}
			log.Error("redis BLPOP error", "error", err)
			time.Sleep(time.Second)
			continue
		}
		// result[0] is the key, result[1] is the build ID (formatted string).
		buildIDStr := result[1]
		log.Info("picked up build", "build_id", buildIDStr)
		s.executeBuild(ctx, buildIDStr)
	}
}

func (s *BuildService) executeBuild(ctx context.Context, buildIDStr string) {
	log := slog.With("build_id", buildIDStr)

	buildID, err := id.ParseBuildID(buildIDStr)
	if err != nil {
		log.Error("invalid build ID from queue", "error", err)
		return
	}

	build, err := s.DB.GetTemplateBuild(ctx, buildID)
	if err != nil {
		log.Error("failed to fetch build", "error", err)
		return
	}

	// Mark as running.
	if _, err := s.DB.UpdateBuildStatus(ctx, db.UpdateBuildStatusParams{
		ID: buildID, Status: "running",
	}); err != nil {
		log.Error("failed to update build status", "error", err)
		return
	}

	// Parse user recipe.
	var recipe []string
	if err := json.Unmarshal(build.Recipe, &recipe); err != nil {
		s.failBuild(ctx, buildID, fmt.Sprintf("invalid recipe JSON: %v", err))
		return
	}

	// Pick a platform host and create a sandbox.
	host, err := s.Scheduler.SelectHost(ctx, id.PlatformTeamID, false)
	if err != nil {
		s.failBuild(ctx, buildID, fmt.Sprintf("no host available: %v", err))
		return
	}

	agent, err := s.Pool.GetForHost(host)
	if err != nil {
		s.failBuild(ctx, buildID, fmt.Sprintf("agent client error: %v", err))
		return
	}

	sandboxID := id.NewSandboxID()
	sandboxIDStr := id.FormatSandboxID(sandboxID)
	log = log.With("sandbox_id", sandboxIDStr, "host_id", id.FormatHostID(host.ID))

	resp, err := agent.CreateSandbox(ctx, connect.NewRequest(&pb.CreateSandboxRequest{
		SandboxId:  sandboxIDStr,
		Template:   build.BaseTemplate,
		Vcpus:      build.Vcpus,
		MemoryMb:   build.MemoryMb,
		TimeoutSec: 0,     // no auto-pause for builds
		DiskSizeMb: 5120, // 5 GB for template builds
	}))
	if err != nil {
		s.failBuild(ctx, buildID, fmt.Sprintf("create sandbox failed: %v", err))
		return
	}
	_ = resp

	// Record sandbox/host association.
	_ = s.DB.UpdateBuildSandbox(ctx, db.UpdateBuildSandboxParams{
		ID:        buildID,
		SandboxID: sandboxID,
		HostID:    host.ID,
	})

	// Execute build phases: pre-build → user recipe → post-build.
	var logs []BuildLogEntry
	step := 0

	// Helper to run a list of commands in a given phase.
	// timeout=0 means no timeout (uses parent context).
	runPhase := func(phase string, cmds []string, timeout time.Duration) bool {
		for _, cmd := range cmds {
			step++
			log.Info("executing build step", "phase", phase, "step", step, "cmd", cmd)

			execCtx := ctx
			var cancel context.CancelFunc
			// When no timeout is specified, use 10 minutes as a generous upper
			// bound. The host agent defaults TimeoutSec=0 to 30s, so we must
			// always send an explicit value.
			effectiveTimeout := timeout
			if effectiveTimeout <= 0 {
				effectiveTimeout = 10 * time.Minute
			}
			execCtx, cancel = context.WithTimeout(ctx, effectiveTimeout)
			timeoutSec := int32(effectiveTimeout.Seconds())

			start := time.Now()
			execResp, err := agent.Exec(execCtx, connect.NewRequest(&pb.ExecRequest{
				SandboxId:  sandboxIDStr,
				Cmd:        "/bin/sh",
				Args:       []string{"-c", cmd},
				TimeoutSec: timeoutSec,
			}))
			cancel()

			entry := BuildLogEntry{
				Step:    step,
				Phase:   phase,
				Cmd:     cmd,
				Elapsed: time.Since(start).Milliseconds(),
			}

			if err != nil {
				entry.Stderr = err.Error()
				entry.Ok = false
				logs = append(logs, entry)
				s.updateLogs(ctx, buildID, step, logs)
				s.destroySandbox(ctx, agent, sandboxIDStr)
				s.failBuild(ctx, buildID, fmt.Sprintf("%s step %d failed: %v", phase, step, err))
				return false
			}

			entry.Stdout = string(execResp.Msg.Stdout)
			entry.Stderr = string(execResp.Msg.Stderr)
			entry.Exit = execResp.Msg.ExitCode
			entry.Ok = execResp.Msg.ExitCode == 0
			logs = append(logs, entry)
			s.updateLogs(ctx, buildID, step, logs)

			if execResp.Msg.ExitCode != 0 {
				s.destroySandbox(ctx, agent, sandboxIDStr)
				s.failBuild(ctx, buildID, fmt.Sprintf("%s step %d failed with exit code %d", phase, step, execResp.Msg.ExitCode))
				return false
			}
		}
		return true
	}

	if !runPhase("pre-build", preBuildCmds, 0) {
		return
	}
	if !runPhase("recipe", recipe, buildCommandTimeout) {
		return
	}
	if !runPhase("post-build", postBuildCmds, 0) {
		return
	}

	// Healthcheck or direct snapshot.
	var sizeBytes int64
	if build.Healthcheck != "" {
		log.Info("running healthcheck", "cmd", build.Healthcheck)
		if err := s.waitForHealthcheck(ctx, agent, sandboxIDStr, build.Healthcheck); err != nil {
			s.destroySandbox(ctx, agent, sandboxIDStr)
			s.failBuild(ctx, buildID, fmt.Sprintf("healthcheck failed: %v", err))
			return
		}

		// Healthcheck passed → full snapshot (with memory/CPU state).
		log.Info("healthcheck passed, creating snapshot")
		snapResp, err := agent.CreateSnapshot(ctx, connect.NewRequest(&pb.CreateSnapshotRequest{
			SandboxId: sandboxIDStr,
			Name:      build.Name,
		}))
		if err != nil {
			s.destroySandbox(ctx, agent, sandboxIDStr)
			s.failBuild(ctx, buildID, fmt.Sprintf("create snapshot failed: %v", err))
			return
		}
		sizeBytes = snapResp.Msg.SizeBytes
	} else {
		// No healthcheck → image-only template (rootfs only).
		log.Info("no healthcheck, flattening rootfs")
		flatResp, err := agent.FlattenRootfs(ctx, connect.NewRequest(&pb.FlattenRootfsRequest{
			SandboxId: sandboxIDStr,
			Name:      build.Name,
		}))
		if err != nil {
			s.destroySandbox(ctx, agent, sandboxIDStr)
			s.failBuild(ctx, buildID, fmt.Sprintf("flatten rootfs failed: %v", err))
			return
		}
		sizeBytes = flatResp.Msg.SizeBytes
	}

	// Insert into templates table as a global (platform) template.
	templateType := "base"
	if build.Healthcheck != "" {
		templateType = "snapshot"
	}

	if _, err := s.DB.InsertTemplate(ctx, db.InsertTemplateParams{
		Name:      build.Name,
		Type:      templateType,
		Vcpus:     build.Vcpus,
		MemoryMb:  build.MemoryMb,
		SizeBytes: sizeBytes,
		TeamID:    id.PlatformTeamID,
	}); err != nil {
		log.Error("failed to insert template record", "error", err)
		// Build succeeded on disk, just DB record failed — don't mark as failed.
	}

	// For CreateSnapshot, the sandbox is already destroyed by the snapshot process.
	// For FlattenRootfs, the sandbox is already destroyed by the flatten process.
	// No additional destroy needed.

	// Mark build as success.
	if _, err := s.DB.UpdateBuildStatus(ctx, db.UpdateBuildStatusParams{
		ID: buildID, Status: "success",
	}); err != nil {
		log.Error("failed to mark build as success", "error", err)
	}

	log.Info("template build completed successfully", "name", build.Name)
}

func (s *BuildService) waitForHealthcheck(ctx context.Context, agent buildAgentClient, sandboxIDStr, cmd string) error {
	deadline := time.NewTimer(healthcheckTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(healthcheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("healthcheck timed out after %s", healthcheckTimeout)
		case <-ticker.C:
			execCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			resp, err := agent.Exec(execCtx, connect.NewRequest(&pb.ExecRequest{
				SandboxId:  sandboxIDStr,
				Cmd:        "/bin/sh",
				Args:       []string{"-c", cmd},
				TimeoutSec: 10,
			}))
			cancel()

			if err != nil {
				slog.Debug("healthcheck exec error (retrying)", "error", err)
				continue
			}
			if resp.Msg.ExitCode == 0 {
				return nil
			}
			slog.Debug("healthcheck failed (retrying)", "exit_code", resp.Msg.ExitCode)
		}
	}
}

func (s *BuildService) updateLogs(ctx context.Context, buildID pgtype.UUID, step int, logs []BuildLogEntry) {
	logsJSON, err := json.Marshal(logs)
	if err != nil {
		slog.Warn("failed to marshal build logs", "error", err)
		return
	}
	if err := s.DB.UpdateBuildProgress(ctx, db.UpdateBuildProgressParams{
		ID:          buildID,
		CurrentStep: int32(step),
		Logs:        logsJSON,
	}); err != nil {
		slog.Warn("failed to update build progress", "error", err)
	}
}

func (s *BuildService) failBuild(_ context.Context, buildID pgtype.UUID, errMsg string) {
	slog.Error("build failed", "build_id", id.FormatBuildID(buildID), "error", errMsg)
	// Use a detached context so DB writes survive parent context cancellation (e.g. shutdown).
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.DB.UpdateBuildError(ctx, db.UpdateBuildErrorParams{
		ID:    buildID,
		Error: errMsg,
	}); err != nil {
		slog.Error("failed to update build error", "build_id", id.FormatBuildID(buildID), "error", err)
	}
}

func (s *BuildService) destroySandbox(_ context.Context, agent buildAgentClient, sandboxIDStr string) {
	// Use a detached context so cleanup succeeds even during shutdown.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := agent.DestroySandbox(ctx, connect.NewRequest(&pb.DestroySandboxRequest{
		SandboxId: sandboxIDStr,
	})); err != nil {
		slog.Warn("failed to destroy build sandbox", "sandbox_id", sandboxIDStr, "error", err)
	}
}
