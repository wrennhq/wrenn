package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"git.omukk.dev/wrenn/wrenn/internal/recipe"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/scheduler"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

const (
	buildQueueKey       = "wrenn:build_queue"
	buildCommandTimeout = 30 * time.Second
)

// preBuildCmds run before the recipe to prepare the build environment, as
// root. The build user (USER/WORKDIR) is not injected here — Create prepends
// it to the persisted recipe instead, so "run as root" can omit it with no
// build-level flag to track.
var preBuildCmds = []string{
	"RUN apt update",
}

// buildUser is the non-root user a recipe runs as unless run_as_root is set.
const buildUser = "wrenn-user"

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
	PtyAttach(ctx context.Context, req *connect.Request[pb.PtyAttachRequest]) (*connect.ServerStreamForClient[pb.PtyAttachResponse], error)
	PtyKill(ctx context.Context, req *connect.Request[pb.PtyKillRequest]) (*connect.Response[pb.PtyKillResponse], error)
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
	RunAsRoot    bool   // Run the recipe as root instead of the non-root build user.
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

	// Assemble the recipe. Unless run_as_root is set, the non-root build user
	// is prepended as USER + WORKDIR steps. Persisting it in the recipe means
	// "run as root" needs no build-level flag — it simply omits these steps,
	// so wrenn-user is never created in a root template.
	recipeLines := p.Recipe
	if !p.RunAsRoot {
		recipeLines = append([]string{
			"USER " + buildUser,
			"WORKDIR /home/" + buildUser,
		}, recipeLines...)
	}

	recipeJSON, err := json.Marshal(recipeLines)
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
		TotalSteps:   int32(len(recipeLines) + defaultSteps),
		TemplateID:   newTemplateID,
		TeamID:       id.PlatformTeamID,
		SkipPrePost:  p.SkipPrePost,
	})
	if err != nil {
		return db.TemplateBuild{}, fmt.Errorf("insert build: %w", err)
	}

	// Store archive before enqueue so the worker never dequeues without files.
	if len(p.Archive) > 0 {
		s.storeArchive(buildIDStr, p.Archive)
	}

	if err := s.Redis.RPush(ctx, buildQueueKey, buildIDStr).Err(); err != nil {
		s.takeArchive(buildIDStr) // clean up on enqueue failure
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
	s.publishStatus(ctx, buildID, "cancelled", 0, 0, "")

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
	s.publishStatus(buildCtx, buildID, "running", 0, build.TotalSteps, "")

	// Parse user recipe.
	var userRecipe []string
	if err := json.Unmarshal(build.Recipe, &userRecipe); err != nil {
		s.failBuild(buildCtx, buildID, fmt.Sprintf("invalid recipe JSON: %v", err))
		return
	}

	agent, sandboxIDStr, sandboxMetadata, err := s.provisionBuildSandbox(buildCtx, buildID, buildIDStr, build, log)
	if err != nil {
		return
	}
	log = log.With("sandbox_id", sandboxIDStr)

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

	streamFn := s.ptyStreamExec(agent)

	runPhase := func(phase string, steps []recipe.Step, defaultTimeout time.Duration) bool {
		// step-start: published before each step begins.
		onStepStart := func(stepNum int, ph string, st recipe.Step) {
			publishBuildEvent(buildCtx, s.Redis, buildIDStr, BuildStreamEvent{
				Type: "step-start", Step: stepNum, Phase: ph, Cmd: st.Raw,
			})
		}
		// output: raw PTY bytes from a streaming RUN step, base64-encoded.
		onChunk := func(stepNum int, data []byte) {
			publishBuildEvent(buildCtx, s.Redis, buildIDStr, BuildStreamEvent{
				Type: "output", Step: stepNum, Data: base64.StdEncoding.EncodeToString(data),
			})
		}
		// onProgress: persist the DB log snapshot and publish step-end.
		onProgress := func(currentStep int, phaseEntries []recipe.BuildLogEntry) {
			s.updateLogs(buildCtx, buildID, currentStep, append(logs, phaseEntries...))
			if len(phaseEntries) > 0 {
				last := phaseEntries[len(phaseEntries)-1]
				publishBuildEvent(buildCtx, s.Redis, buildIDStr, BuildStreamEvent{
					Type: "step-end", Step: last.Step, Phase: last.Phase, Cmd: last.Cmd,
					Exit: last.Exit, Ok: last.Ok, ElapsedMs: last.Elapsed,
				})
			}
		}

		newEntries, nextStep, ok := recipe.Execute(buildCtx, phase, steps, sandboxIDStr, step,
			defaultTimeout, bctx, agent.Exec, streamFn, onStepStart, onChunk, onProgress)
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

	// Phase 1: Pre-build (as root) — apt update.
	if !build.SkipPrePost {
		if !runPhase("pre-build", preBuildSteps, 0) {
			return
		}
	}

	// Phase 2: Recipe — the persisted recipe. For non-root builds it begins
	// with the injected USER/WORKDIR steps that create and switch to the build
	// user; for run_as_root builds it runs as root throughout.
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

	// Finalize: healthcheck/snapshot/flatten → persist template → mark success.
	s.finalizeBuild(buildCtx, buildID, build, agent, sandboxIDStr, templateDefaultUser, templateDefaultEnv, sandboxMetadata, log)
}

// provisionBuildSandbox picks a host, creates a sandbox, and uploads the build
// archive. On failure it calls failBuild and returns an error.
func (s *BuildService) provisionBuildSandbox(
	ctx context.Context,
	buildID pgtype.UUID,
	buildIDStr string,
	build db.TemplateBuild,
	log *slog.Logger,
) (buildAgentClient, string, map[string]string, error) {
	host, err := s.Scheduler.SelectHost(ctx, id.PlatformTeamID, false, build.MemoryMb, 5120)
	if err != nil {
		s.failBuild(ctx, buildID, fmt.Sprintf("no host available: %v", err))
		return nil, "", nil, err
	}

	agent, err := s.Pool.GetForHost(host)
	if err != nil {
		s.failBuild(ctx, buildID, fmt.Sprintf("agent client error: %v", err))
		return nil, "", nil, err
	}

	sandboxID := id.NewSandboxID()
	sandboxIDStr := id.FormatSandboxID(sandboxID)
	log.Info("provisioning build sandbox", "sandbox_id", sandboxIDStr, "host_id", id.FormatHostID(host.ID))

	baseTeamID := id.PlatformTeamID
	baseTemplateID := id.MinimalTemplateID
	if build.BaseTemplate != "minimal" {
		baseTmpl, err := s.DB.GetPlatformTemplateByName(ctx, build.BaseTemplate)
		if err != nil {
			s.failBuild(ctx, buildID, fmt.Sprintf("base template %q not found: %v", build.BaseTemplate, err))
			return nil, "", nil, err
		}
		baseTeamID = baseTmpl.TeamID
		baseTemplateID = baseTmpl.ID
	}

	resp, err := agent.CreateSandbox(ctx, connect.NewRequest(&pb.CreateSandboxRequest{
		SandboxId:  sandboxIDStr,
		Template:   build.BaseTemplate,
		TeamId:     id.UUIDString(baseTeamID),
		TemplateId: id.UUIDString(baseTemplateID),
		Vcpus:      build.Vcpus,
		MemoryMb:   build.MemoryMb,
		TimeoutSec: 0,
		DiskSizeMb: 5120,
	}))
	if err != nil {
		s.failBuild(ctx, buildID, fmt.Sprintf("create sandbox failed: %v", err))
		return nil, "", nil, err
	}
	sandboxMetadata := resp.Msg.Metadata

	_ = s.DB.UpdateBuildSandbox(ctx, db.UpdateBuildSandboxParams{
		ID:        buildID,
		SandboxID: sandboxID,
		HostID:    host.ID,
	})

	archive := s.takeArchive(buildIDStr)
	if len(archive) > 0 {
		if err := s.uploadAndExtractArchive(ctx, agent, sandboxIDStr, archive, buildIDStr); err != nil {
			s.destroySandbox(ctx, agent, sandboxIDStr)
			s.failBuild(ctx, buildID, fmt.Sprintf("archive upload failed: %v", err))
			return nil, "", nil, err
		}
	}

	return agent, sandboxIDStr, sandboxMetadata, nil
}

// finalizeBuild handles the healthcheck/snapshot/flatten step and persists the
// template record. Called after all recipe phases complete successfully.
func (s *BuildService) finalizeBuild(
	ctx context.Context,
	buildID pgtype.UUID,
	build db.TemplateBuild,
	agent buildAgentClient,
	sandboxIDStr string,
	defaultUser string,
	defaultEnv map[string]string,
	sandboxMetadata map[string]string,
	log *slog.Logger,
) {
	var sizeBytes int64
	if build.Healthcheck != "" {
		hc, err := recipe.ParseHealthcheck(build.Healthcheck)
		if err != nil {
			s.destroySandbox(ctx, agent, sandboxIDStr)
			s.failBuild(ctx, buildID, fmt.Sprintf("invalid healthcheck: %v", err))
			return
		}
		log.Info("running healthcheck", "cmd", hc.Cmd, "interval", hc.Interval, "timeout", hc.Timeout, "start_period", hc.StartPeriod, "retries", hc.Retries)
		if err := s.waitForHealthcheck(ctx, agent, sandboxIDStr, hc, defaultUser); err != nil {
			s.destroySandbox(ctx, agent, sandboxIDStr)
			if ctx.Err() != nil {
				return
			}
			s.failBuild(ctx, buildID, fmt.Sprintf("healthcheck failed: %v", err))
			return
		}

		log.Info("healthcheck passed, creating snapshot")
		snapResp, err := agent.CreateSnapshot(ctx, connect.NewRequest(&pb.CreateSnapshotRequest{
			SandboxId:  sandboxIDStr,
			Name:       build.Name,
			TeamId:     id.UUIDString(build.TeamID),
			TemplateId: id.UUIDString(build.TemplateID),
		}))
		if err != nil {
			s.destroySandbox(ctx, agent, sandboxIDStr)
			if ctx.Err() != nil {
				return
			}
			s.failBuild(ctx, buildID, fmt.Sprintf("create snapshot failed: %v", err))
			return
		}
		sizeBytes = snapResp.Msg.SizeBytes
	} else {
		log.Info("no healthcheck, flattening rootfs")
		flatResp, err := agent.FlattenRootfs(ctx, connect.NewRequest(&pb.FlattenRootfsRequest{
			SandboxId:  sandboxIDStr,
			Name:       build.Name,
			TeamId:     id.UUIDString(build.TeamID),
			TemplateId: id.UUIDString(build.TemplateID),
		}))
		if err != nil {
			s.destroySandbox(ctx, agent, sandboxIDStr)
			if ctx.Err() != nil {
				return
			}
			s.failBuild(ctx, buildID, fmt.Sprintf("flatten rootfs failed: %v", err))
			return
		}
		sizeBytes = flatResp.Msg.SizeBytes
	}

	templateType := "base"
	if build.Healthcheck != "" {
		templateType = "snapshot"
	}

	defaultEnvJSON, err := json.Marshal(defaultEnv)
	if err != nil {
		defaultEnvJSON = []byte("{}")
	}
	metadataJSON, err := json.Marshal(sandboxMetadata)
	if err != nil || len(sandboxMetadata) == 0 {
		metadataJSON = []byte("{}")
	}

	if _, err := s.DB.InsertTemplate(ctx, db.InsertTemplateParams{
		ID:          build.TemplateID,
		Name:        build.Name,
		Type:        templateType,
		Vcpus:       build.Vcpus,
		MemoryMb:    build.MemoryMb,
		SizeBytes:   sizeBytes,
		TeamID:      id.PlatformTeamID,
		DefaultUser: defaultUser,
		DefaultEnv:  defaultEnvJSON,
		Metadata:    metadataJSON,
	}); err != nil {
		log.Error("failed to insert template record", "error", err)
	}

	_ = s.DB.UpdateBuildDefaults(ctx, db.UpdateBuildDefaultsParams{
		ID:          buildID,
		DefaultUser: defaultUser,
		DefaultEnv:  defaultEnvJSON,
		Metadata:    metadataJSON,
	})

	if _, err := s.DB.UpdateBuildStatus(ctx, db.UpdateBuildStatusParams{
		ID: buildID, Status: "success",
	}); err != nil {
		log.Error("failed to mark build as success", "error", err)
	}
	s.publishStatus(ctx, buildID, "success", build.TotalSteps, build.TotalSteps, "")

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
	s.publishStatus(ctx, buildID, "failed", 0, 0, errMsg)
}

// build PTY dimensions — wide enough for tools that adapt output to terminal
// width (apt/pip progress bars).
const (
	buildPtyCols = 120
	buildPtyRows = 40
)

// publishStatus emits a build-status event to the build's live stream.
func (s *BuildService) publishStatus(ctx context.Context, buildID pgtype.UUID, status string, currentStep, totalSteps int32, errMsg string) {
	publishBuildEvent(ctx, s.Redis, id.FormatBuildID(buildID), BuildStreamEvent{
		Type:        "build-status",
		Status:      status,
		CurrentStep: currentStep,
		TotalSteps:  totalSteps,
		Error:       errMsg,
	})
}

// ptyStreamExec returns a recipe.StreamExecFunc that runs a shell command in a
// PTY on the build sandbox via the host agent and streams its output. A PTY
// makes build tools emit unbuffered, colorized output (apt/pip progress bars).
func (s *BuildService) ptyStreamExec(agent buildAgentClient) recipe.StreamExecFunc {
	return func(ctx context.Context, sandboxID, shellCmd string) (<-chan recipe.PtyChunk, error) {
		tag := "build-" + id.NewPtyTag()
		stream, err := agent.PtyAttach(ctx, connect.NewRequest(&pb.PtyAttachRequest{
			SandboxId: sandboxID,
			Tag:       tag,
			Cmd:       "/bin/sh",
			Args:      []string{"-c", shellCmd},
			Cols:      buildPtyCols,
			Rows:      buildPtyRows,
		}))
		if err != nil {
			return nil, err
		}

		ch := make(chan recipe.PtyChunk, 64)
		go func() {
			defer close(ch)
			defer stream.Close()

			gotExit := false
			for stream.Receive() {
				switch ev := stream.Msg().Event.(type) {
				case *pb.PtyAttachResponse_Output:
					select {
					case ch <- recipe.PtyChunk{Data: ev.Output.Data}:
					case <-ctx.Done():
						return
					}
				case *pb.PtyAttachResponse_Exited:
					gotExit = true
					select {
					case ch <- recipe.PtyChunk{Done: true, Exit: ev.Exited.ExitCode}:
					case <-ctx.Done():
						return
					}
				}
			}
			if gotExit {
				return
			}
			// Stream ended with no exit event: timeout, cancellation, or error.
			// Kill the lingering guest process so it does not keep running.
			streamErr := stream.Err()
			if ctx.Err() != nil {
				killCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, _ = agent.PtyKill(killCtx, connect.NewRequest(&pb.PtyKillRequest{
					SandboxId: sandboxID, Tag: tag,
				}))
				cancel()
				if streamErr == nil {
					streamErr = ctx.Err()
				}
			}
			if streamErr == nil {
				streamErr = fmt.Errorf("pty stream ended without an exit event")
			}
			ch <- recipe.PtyChunk{Err: streamErr}
		}()
		return ch, nil
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
	// Per-sandbox identifiers set by envd at boot via PostInit.
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
