package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/id"
	"git.omukk.dev/wrenn/wrenn/internal/lifecycle"
	"git.omukk.dev/wrenn/wrenn/internal/recipe"
	"git.omukk.dev/wrenn/wrenn/internal/scheduler"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

const (
	buildQueueKey       = "wrenn:build_queue"
	buildCommandTimeout = 30 * time.Second
)

// preBuildCmds run before the user recipe to prepare the build environment.
// apt update runs as root first, then USER switches to wrenn-user for the recipe.
var preBuildCmds = []string{
	"RUN apt update",
	"USER wrenn-user",
	"WORKDIR /home/wrenn-user",
}

// postBuildCmds run after the user recipe to clean up caches and reduce image size.
var postBuildCmds = []string{
	"RUN apt clean",
	"RUN apt autoremove -y",
	"RUN rm -rf /var/lib/apt/lists/*",
	"RUN rm -rf /tmp/build-files /tmp/build-files.*",
}

// buildAgentClient is the subset of the host agent client used by the build worker.
type buildAgentClient interface {
	CreateSandbox(ctx context.Context, req *connect.Request[pb.CreateSandboxRequest]) (*connect.Response[pb.CreateSandboxResponse], error)
	DestroySandbox(ctx context.Context, req *connect.Request[pb.DestroySandboxRequest]) (*connect.Response[pb.DestroySandboxResponse], error)
	Exec(ctx context.Context, req *connect.Request[pb.ExecRequest]) (*connect.Response[pb.ExecResponse], error)
	WriteFile(ctx context.Context, req *connect.Request[pb.WriteFileRequest]) (*connect.Response[pb.WriteFileResponse], error)
	CreateSnapshot(ctx context.Context, req *connect.Request[pb.CreateSnapshotRequest]) (*connect.Response[pb.CreateSnapshotResponse], error)
	FlattenRootfs(ctx context.Context, req *connect.Request[pb.FlattenRootfsRequest]) (*connect.Response[pb.FlattenRootfsResponse], error)
}

// BuildService handles template build orchestration.
type BuildService struct {
	DB        *db.Queries
	Redis     *redis.Client
	Pool      *lifecycle.HostClientPool
	Scheduler scheduler.HostScheduler

	mu        sync.Mutex
	cancelMap map[string]context.CancelFunc // buildID → per-build cancel func
	filesMap  map[string][]byte             // buildID → uploaded archive bytes
}

// BuildCreateParams holds the parameters for creating a template build.
type BuildCreateParams struct {
	Name         string
	BaseTemplate string
	Recipe       []string
	Healthcheck  string
	VCPUs        int32
	MemoryMB     int32
	SkipPrePost  bool
	Archive      []byte // Optional tar/tar.gz/zip archive for COPY commands.
	ArchiveName  string // Original filename (used to detect format).
}

// storeArchive stores uploaded archive bytes keyed by build ID for the worker.
func (s *BuildService) storeArchive(buildID string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.filesMap == nil {
		s.filesMap = make(map[string][]byte)
	}
	s.filesMap[buildID] = data
}

// takeArchive retrieves and removes stored archive bytes for a build.
func (s *BuildService) takeArchive(buildID string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	data := s.filesMap[buildID]
	delete(s.filesMap, buildID)
	return data
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
	newTemplateID := id.NewTemplateID()

	defaultSteps := len(preBuildCmds) + len(postBuildCmds)
	if p.SkipPrePost {
		defaultSteps = 0
	}

	build, err := s.DB.InsertTemplateBuild(ctx, db.InsertTemplateBuildParams{
		ID:           buildID,
		Name:         p.Name,
		BaseTemplate: p.BaseTemplate,
		Recipe:       recipeJSON,
		Healthcheck:  p.Healthcheck,
		Vcpus:        p.VCPUs,
		MemoryMb:     p.MemoryMB,
		TotalSteps:   int32(len(p.Recipe) + defaultSteps),
		TemplateID:   newTemplateID,
		TeamID:       id.PlatformTeamID,
		SkipPrePost:  p.SkipPrePost,
	})
	if err != nil {
		return db.TemplateBuild{}, fmt.Errorf("insert build: %w", err)
	}

	// Enqueue build ID (as formatted string) to Redis for workers to pick up.
	if err := s.Redis.RPush(ctx, buildQueueKey, buildIDStr).Err(); err != nil {
		return db.TemplateBuild{}, fmt.Errorf("enqueue build: %w", err)
	}

	// Store archive for the worker if provided.
	if len(p.Archive) > 0 {
		s.storeArchive(buildIDStr, p.Archive)
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

// Cancel cancels a pending or running build. For pending builds the status is
// updated in the DB and the worker skips it when dequeued. For running builds
// the per-build context is cancelled, which causes the current exec step to
// abort; executeBuild then detects the cancellation and records the status.
func (s *BuildService) Cancel(ctx context.Context, buildID pgtype.UUID) error {
	build, err := s.DB.GetTemplateBuild(ctx, buildID)
	if err != nil {
		return fmt.Errorf("get build: %w", err)
	}
	switch build.Status {
	case "success", "failed", "cancelled":
		return fmt.Errorf("build is already %s", build.Status)
	}

	// Mark cancelled in DB first. This handles both pending builds (which haven't
	// been picked up yet) and acts as a flag for executeBuild to check on start.
	if _, err := s.DB.UpdateBuildStatus(ctx, db.UpdateBuildStatusParams{
		ID: buildID, Status: "cancelled",
	}); err != nil {
		return fmt.Errorf("update build status: %w", err)
	}

	// If the build is currently running, signal its context.
	buildIDStr := id.FormatBuildID(buildID)
	s.mu.Lock()
	cancel, running := s.cancelMap[buildIDStr]
	s.mu.Unlock()
	if running {
		cancel()
	}

	return nil
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

	// Create a per-build context so this build can be cancelled independently of
	// the worker. Register in cancelMap before fetching the build so that a
	// concurrent Cancel call can always find and signal it.
	buildCtx, buildCancel := context.WithCancel(ctx)
	defer buildCancel()

	s.mu.Lock()
	if s.cancelMap == nil {
		s.cancelMap = make(map[string]context.CancelFunc)
	}
	s.cancelMap[buildIDStr] = buildCancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.cancelMap, buildIDStr)
		s.mu.Unlock()
	}()

	build, err := s.DB.GetTemplateBuild(buildCtx, buildID)
	if err != nil {
		log.Error("failed to fetch build", "error", err)
		return
	}

	// Skip if already cancelled (Cancel was called before we dequeued).
	if build.Status == "cancelled" {
		log.Info("build already cancelled, skipping")
		return
	}

	// Mark as running.
	if _, err := s.DB.UpdateBuildStatus(buildCtx, db.UpdateBuildStatusParams{
		ID: buildID, Status: "running",
	}); err != nil {
		log.Error("failed to update build status", "error", err)
		return
	}

	// Parse user recipe.
	var userRecipe []string
	if err := json.Unmarshal(build.Recipe, &userRecipe); err != nil {
		s.failBuild(buildCtx, buildID, fmt.Sprintf("invalid recipe JSON: %v", err))
		return
	}

	// Pick a platform host and create a sandbox.
	host, err := s.Scheduler.SelectHost(buildCtx, id.PlatformTeamID, false, build.MemoryMb, 5120)
	if err != nil {
		s.failBuild(buildCtx, buildID, fmt.Sprintf("no host available: %v", err))
		return
	}

	agent, err := s.Pool.GetForHost(host)
	if err != nil {
		s.failBuild(buildCtx, buildID, fmt.Sprintf("agent client error: %v", err))
		return
	}

	sandboxID := id.NewSandboxID()
	sandboxIDStr := id.FormatSandboxID(sandboxID)
	log = log.With("sandbox_id", sandboxIDStr, "host_id", id.FormatHostID(host.ID))

	// Resolve the base template to UUIDs. "minimal" is the zero sentinel.
	baseTeamID := id.PlatformTeamID
	baseTemplateID := id.MinimalTemplateID
	if build.BaseTemplate != "minimal" {
		baseTmpl, err := s.DB.GetPlatformTemplateByName(buildCtx, build.BaseTemplate)
		if err != nil {
			s.failBuild(buildCtx, buildID, fmt.Sprintf("base template %q not found: %v", build.BaseTemplate, err))
			return
		}
		baseTeamID = baseTmpl.TeamID
		baseTemplateID = baseTmpl.ID
	}

	resp, err := agent.CreateSandbox(buildCtx, connect.NewRequest(&pb.CreateSandboxRequest{
		SandboxId:  sandboxIDStr,
		Template:   build.BaseTemplate,
		TeamId:     id.UUIDString(baseTeamID),
		TemplateId: id.UUIDString(baseTemplateID),
		Vcpus:      build.Vcpus,
		MemoryMb:   build.MemoryMb,
		TimeoutSec: 0,    // no auto-pause for builds
		DiskSizeMb: 5120, // 5 GB for template builds
	}))
	if err != nil {
		s.failBuild(buildCtx, buildID, fmt.Sprintf("create sandbox failed: %v", err))
		return
	}
	_ = resp

	// Record sandbox/host association.
	_ = s.DB.UpdateBuildSandbox(buildCtx, db.UpdateBuildSandboxParams{
		ID:        buildID,
		SandboxID: sandboxID,
		HostID:    host.ID,
	})

	// Upload and extract build archive if provided.
	archive := s.takeArchive(buildIDStr)
	if len(archive) > 0 {
		if err := s.uploadAndExtractArchive(buildCtx, agent, sandboxIDStr, archive, buildIDStr); err != nil {
			s.destroySandbox(buildCtx, agent, sandboxIDStr)
			s.failBuild(buildCtx, buildID, fmt.Sprintf("archive upload failed: %v", err))
			return
		}
	}

	// Parse recipe steps. preBuildCmds and postBuildCmds are hardcoded and always
	// valid; panic on error is appropriate here since it would be a programmer mistake.
	preBuildSteps, err := recipe.ParseRecipe(preBuildCmds)
	if err != nil {
		panic(fmt.Sprintf("invalid pre-build recipe: %v", err))
	}
	userRecipeSteps, err := recipe.ParseRecipe(userRecipe)
	if err != nil {
		s.destroySandbox(buildCtx, agent, sandboxIDStr)
		s.failBuild(buildCtx, buildID, fmt.Sprintf("recipe parse error: %v", err))
		return
	}
	postBuildSteps, err := recipe.ParseRecipe(postBuildCmds)
	if err != nil {
		panic(fmt.Sprintf("invalid post-build recipe: %v", err))
	}

	var logs []recipe.BuildLogEntry
	step := 0

	envVars, err := s.fetchSandboxEnv(buildCtx, agent, sandboxIDStr)
	if err != nil {
		log.Warn("failed to fetch sandbox env, using defaults", "error", err)
		envVars = map[string]string{
			"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"HOME": "/root",
		}
	}
	bctx := &recipe.ExecContext{EnvVars: envVars, User: "root"}

	// Per-step progress callback for live UI updates.
	progressFn := func(currentStep int, allEntries []recipe.BuildLogEntry) {
		s.updateLogs(buildCtx, buildID, currentStep, allEntries)
	}

	runPhase := func(phase string, steps []recipe.Step, defaultTimeout time.Duration) bool {
		newEntries, nextStep, ok := recipe.Execute(buildCtx, phase, steps, sandboxIDStr, step, defaultTimeout, bctx, agent.Exec, func(currentStep int, phaseEntries []recipe.BuildLogEntry) {
			// Progress callback: combine prior logs with current phase entries.
			progressFn(currentStep, append(logs, phaseEntries...))
		})
		logs = append(logs, newEntries...)
		step = nextStep
		s.updateLogs(buildCtx, buildID, step, logs)
		if !ok {
			s.destroySandbox(buildCtx, agent, sandboxIDStr)
			// If the build was cancelled, status is already set — don't overwrite with "failed".
			if buildCtx.Err() != nil {
				return false
			}
			reason := "unknown error"
			if len(newEntries) > 0 {
				last := newEntries[len(newEntries)-1]
				reason = last.Stderr
				if reason == "" {
					reason = fmt.Sprintf("exit code %d", last.Exit)
				}
			}
			s.failBuild(buildCtx, buildID, fmt.Sprintf("%s step %d failed: %s", phase, step, reason))
		}
		return ok
	}

	// Phase 1: Pre-build (as root) — creates wrenn-user, updates apt.
	if !build.SkipPrePost {
		if !runPhase("pre-build", preBuildSteps, 0) {
			return
		}
	}

	// Phase 2: User recipe — starts as wrenn-user (set by USER in pre-build)
	// or root if skip_pre_post.
	if !runPhase("recipe", userRecipeSteps, buildCommandTimeout) {
		return
	}

	// Capture the final user and env vars as template defaults.
	// Filter out user-specific and runtime vars that should be resolved at
	// sandbox creation time, not baked in from the build environment.
	templateDefaultUser := bctx.User
	templateDefaultEnv := filterBuildEnv(bctx.EnvVars)

	// Phase 3: Post-build (as root) — cleanup.
	bctx.User = "root"
	if !build.SkipPrePost {
		if !runPhase("post-build", postBuildSteps, 0) {
			return
		}
	}

	// Healthcheck or direct snapshot.
	var sizeBytes int64
	if build.Healthcheck != "" {
		hc, err := recipe.ParseHealthcheck(build.Healthcheck)
		if err != nil {
			s.destroySandbox(buildCtx, agent, sandboxIDStr)
			s.failBuild(buildCtx, buildID, fmt.Sprintf("invalid healthcheck: %v", err))
			return
		}
		log.Info("running healthcheck", "cmd", hc.Cmd, "interval", hc.Interval, "timeout", hc.Timeout, "start_period", hc.StartPeriod, "retries", hc.Retries)
		if err := s.waitForHealthcheck(buildCtx, agent, sandboxIDStr, hc, templateDefaultUser); err != nil {
			s.destroySandbox(buildCtx, agent, sandboxIDStr)
			if buildCtx.Err() != nil {
				return
			}
			s.failBuild(buildCtx, buildID, fmt.Sprintf("healthcheck failed: %v", err))
			return
		}

		// Healthcheck passed → full snapshot (with memory/CPU state).
		log.Info("healthcheck passed, creating snapshot")
		snapResp, err := agent.CreateSnapshot(buildCtx, connect.NewRequest(&pb.CreateSnapshotRequest{
			SandboxId:  sandboxIDStr,
			Name:       build.Name,
			TeamId:     id.UUIDString(build.TeamID),
			TemplateId: id.UUIDString(build.TemplateID),
		}))
		if err != nil {
			s.destroySandbox(buildCtx, agent, sandboxIDStr)
			if buildCtx.Err() != nil {
				return
			}
			s.failBuild(buildCtx, buildID, fmt.Sprintf("create snapshot failed: %v", err))
			return
		}
		sizeBytes = snapResp.Msg.SizeBytes
	} else {
		// No healthcheck → image-only template (rootfs only).
		log.Info("no healthcheck, flattening rootfs")
		flatResp, err := agent.FlattenRootfs(buildCtx, connect.NewRequest(&pb.FlattenRootfsRequest{
			SandboxId:  sandboxIDStr,
			Name:       build.Name,
			TeamId:     id.UUIDString(build.TeamID),
			TemplateId: id.UUIDString(build.TemplateID),
		}))
		if err != nil {
			s.destroySandbox(buildCtx, agent, sandboxIDStr)
			if buildCtx.Err() != nil {
				return
			}
			s.failBuild(buildCtx, buildID, fmt.Sprintf("flatten rootfs failed: %v", err))
			return
		}
		sizeBytes = flatResp.Msg.SizeBytes
	}

	// Insert into templates table as a global (platform) template.
	templateType := "base"
	if build.Healthcheck != "" {
		templateType = "snapshot"
	}

	// Serialize env vars for DB storage.
	defaultEnvJSON, err := json.Marshal(templateDefaultEnv)
	if err != nil {
		defaultEnvJSON = []byte("{}")
	}

	if _, err := s.DB.InsertTemplate(buildCtx, db.InsertTemplateParams{
		ID:          build.TemplateID,
		Name:        build.Name,
		Type:        templateType,
		Vcpus:       build.Vcpus,
		MemoryMb:    build.MemoryMb,
		SizeBytes:   sizeBytes,
		TeamID:      id.PlatformTeamID,
		DefaultUser: templateDefaultUser,
		DefaultEnv:  defaultEnvJSON,
	}); err != nil {
		log.Error("failed to insert template record", "error", err)
		// Build succeeded on disk, just DB record failed — don't mark as failed.
	}

	// Record defaults on the build record for inspection.
	_ = s.DB.UpdateBuildDefaults(buildCtx, db.UpdateBuildDefaultsParams{
		ID:          buildID,
		DefaultUser: templateDefaultUser,
		DefaultEnv:  defaultEnvJSON,
	})

	// For CreateSnapshot, the sandbox is already destroyed by the snapshot process.
	// For FlattenRootfs, the sandbox is already destroyed by the flatten process.
	// No additional destroy needed.

	// Mark build as success.
	if _, err := s.DB.UpdateBuildStatus(buildCtx, db.UpdateBuildStatusParams{
		ID: buildID, Status: "success",
	}); err != nil {
		log.Error("failed to mark build as success", "error", err)
	}

	log.Info("template build completed successfully", "name", build.Name)
}

// waitForHealthcheck repeatedly executes the healthcheck command inside the
// sandbox according to the config's interval, timeout, start-period, and
// retries.
// During the start period, failures are not counted toward the retry budget.
// Returns nil on the first successful check, or an error if retries are
// exhausted, the deadline passes, or the context is cancelled.
func (s *BuildService) waitForHealthcheck(ctx context.Context, agent buildAgentClient, sandboxIDStr string, hc recipe.HealthcheckConfig, user string) error {
	// Wrap the healthcheck command with su when a non-root user is set, so that
	// ~ expands to the correct home directory and the process runs with the
	// right UID (matching the template's default user).
	cmd := hc.Cmd
	if user != "" && user != "root" {
		cmd = "su " + recipe.Shellescape(user) + " -s /bin/sh -c " + recipe.Shellescape(hc.Cmd)
	}
	ticker := time.NewTicker(hc.Interval)
	defer ticker.Stop()

	// When retries > 0, set a deadline based on the retry budget.
	// When retries == 0 (unlimited), rely solely on the parent context deadline.
	var deadlineCh <-chan time.Time
	if hc.Retries > 0 {
		deadline := time.NewTimer(hc.StartPeriod + time.Duration(hc.Retries+1)*hc.Interval)
		defer deadline.Stop()
		deadlineCh = deadline.C
	}

	startedAt := time.Now()
	failCount := 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadlineCh:
			return fmt.Errorf("healthcheck timed out: exceeded %d attempts over %s", failCount, time.Since(startedAt))
		case <-ticker.C:
			execCtx, cancel := context.WithTimeout(ctx, hc.Timeout)
			resp, err := agent.Exec(execCtx, connect.NewRequest(&pb.ExecRequest{
				SandboxId:  sandboxIDStr,
				Cmd:        "/bin/sh",
				Args:       []string{"-c", cmd},
				TimeoutSec: int32(hc.Timeout.Seconds()),
			}))
			cancel()

			if err != nil {
				slog.Debug("healthcheck exec error (retrying)", "error", err)
				if time.Since(startedAt) >= hc.StartPeriod {
					failCount++
					if hc.Retries > 0 && failCount >= hc.Retries {
						return fmt.Errorf("healthcheck failed after %d retries: exec error: %w", failCount, err)
					}
				}
				continue
			}
			if resp.Msg.ExitCode == 0 {
				return nil
			}
			slog.Debug("healthcheck failed (retrying)", "exit_code", resp.Msg.ExitCode)
			if time.Since(startedAt) >= hc.StartPeriod {
				failCount++
				if hc.Retries > 0 && failCount >= hc.Retries {
					return fmt.Errorf("healthcheck failed after %d retries: exit code %d", failCount, resp.Msg.ExitCode)
				}
			}
		}
	}
}

func (s *BuildService) updateLogs(ctx context.Context, buildID pgtype.UUID, step int, logs []recipe.BuildLogEntry) {
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

// fetchSandboxEnv executes the 'env' command inside the specified sandbox via
// the build agent and returns environment variables
func (s *BuildService) fetchSandboxEnv(ctx context.Context,
	agent buildAgentClient, sandboxIDStr string) (map[string]string, error) {
	resp, err := agent.Exec(ctx, connect.NewRequest(&pb.ExecRequest{
		SandboxId:  sandboxIDStr,
		Cmd:        "/bin/sh",
		Args:       []string{"-c", "env"},
		TimeoutSec: 10,
	}))
	if err != nil {
		return nil, fmt.Errorf("fetch env: %w", err)
	}

	if resp.Msg.ExitCode != 0 {
		return nil, fmt.Errorf("fetch env: command exited with code %d",
			resp.Msg.ExitCode)
	}

	return parseSandboxEnv(string(resp.Msg.Stdout)), nil
}

// parseSandboxEnv converts the raw newline-separated output of an 'env'
// command into a map.
// It skips empty lines and malformed entries, and correctly handles values
// containing '='.
func parseSandboxEnv(raw string) map[string]string {
	envVars := make(map[string]string)

	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		envVars[parts[0]] = parts[1]
	}

	return envVars
}

// uploadAndExtractArchive writes the archive to the sandbox and extracts it
// to /tmp/build-files/. Detects format from content (tar.gz, tar, zip).
func (s *BuildService) uploadAndExtractArchive(
	ctx context.Context,
	agent buildAgentClient,
	sandboxID string,
	archive []byte,
	buildID string,
) error {
	// Detect archive type from magic bytes.
	var archivePath, extractCmd string
	switch {
	case len(archive) >= 2 && archive[0] == 0x1f && archive[1] == 0x8b:
		// gzip (tar.gz)
		archivePath = "/tmp/build-files.tar.gz"
		extractCmd = "mkdir -p /tmp/build-files && tar xzf /tmp/build-files.tar.gz -C /tmp/build-files"
	case len(archive) >= 4 && string(archive[:4]) == "PK\x03\x04":
		// zip
		archivePath = "/tmp/build-files.zip"
		extractCmd = "mkdir -p /tmp/build-files && unzip -o /tmp/build-files.zip -d /tmp/build-files"
	case len(archive) >= 262 && string(archive[257:262]) == "ustar":
		// tar (ustar magic at offset 257)
		archivePath = "/tmp/build-files.tar"
		extractCmd = "mkdir -p /tmp/build-files && tar xf /tmp/build-files.tar -C /tmp/build-files"
	default:
		// Fallback: try tar.gz
		archivePath = "/tmp/build-files.tar.gz"
		extractCmd = "mkdir -p /tmp/build-files && tar xzf /tmp/build-files.tar.gz -C /tmp/build-files"
	}

	slog.Info("uploading build archive", "build_id", buildID, "path", archivePath, "size", len(archive))

	// Write archive to VM.
	if _, err := agent.WriteFile(ctx, connect.NewRequest(&pb.WriteFileRequest{
		SandboxId: sandboxID,
		Path:      archivePath,
		Content:   archive,
	})); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}

	// Extract and ensure files are readable.
	fullCmd := extractCmd + " && chmod -R a+rX /tmp/build-files"

	resp, err := agent.Exec(ctx, connect.NewRequest(&pb.ExecRequest{
		SandboxId:  sandboxID,
		Cmd:        "/bin/sh",
		Args:       []string{"-c", fullCmd},
		TimeoutSec: 120,
	}))
	if err != nil {
		return fmt.Errorf("extract archive: %w", err)
	}
	if resp.Msg.ExitCode != 0 {
		return fmt.Errorf("extract archive: exit code %d: %s", resp.Msg.ExitCode, string(resp.Msg.Stderr))
	}

	return nil
}

// runtimeEnvVars lists env vars that are user- or session-specific and should
// not be persisted into template defaults. These are resolved at runtime by
// envd based on the actual user and sandbox context.
var runtimeEnvVars = map[string]bool{
	"HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"PWD": true, "OLDPWD": true, "HOSTNAME": true, "TERM": true,
	"SHLVL": true, "_": true,
	// Per-sandbox identifiers set by envd at boot via MMDS.
	"WRENN_SANDBOX_ID": true, "WRENN_TEMPLATE_ID": true,
}

// filterBuildEnv returns a copy of envVars with runtime/user-specific
// variables removed so they don't override envd's per-user resolution.
func filterBuildEnv(envVars map[string]string) map[string]string {
	filtered := make(map[string]string, len(envVars))
	for k, v := range envVars {
		if runtimeEnvVars[k] {
			continue
		}
		filtered[k] = v
	}
	return filtered
}
