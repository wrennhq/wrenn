package recipe

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"

	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

// DefaultStepTimeout is the fallback timeout for RUN steps that carry no
// explicit --timeout flag.
const DefaultStepTimeout = 30 * time.Second

// BuildLogEntry is the per-step record stored in template_builds.logs (JSONB).
type BuildLogEntry struct {
	Step    int    `json:"step"`
	Phase   string `json:"phase"`
	Cmd     string `json:"cmd"`
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Exit    int32  `json:"exit"`
	Ok      bool   `json:"ok"`
	Elapsed int64  `json:"elapsed_ms"`
}

// ExecFunc is the agent.Exec call signature used by the executor. It matches
// the method on the hostagent Connect RPC client.
type ExecFunc func(ctx context.Context, req *connect.Request[pb.ExecRequest]) (*connect.Response[pb.ExecResponse], error)

// Execute runs steps sequentially against sandboxID using execFn.
//
//   - phase labels the log entries (e.g., "pre-build", "recipe", "post-build").
//   - startStep is the 1-based offset so entries are globally numbered across phases.
//   - defaultTimeout applies to RUN steps with no per-step --timeout; 0 → 10 minutes.
//   - bctx is mutated in place as ENV/WORKDIR steps execute, and carries forward
//     into subsequent phases when the caller passes the same pointer.
//
// Returns all log entries appended during this call, the next step counter
// value, and whether all steps succeeded. On false the last entry contains
// failure details; the caller is responsible for destroying the sandbox and
// recording the build error.
func Execute(
	ctx context.Context,
	phase string,
	steps []Step,
	sandboxID string,
	startStep int,
	defaultTimeout time.Duration,
	bctx *ExecContext,
	execFn ExecFunc,
) (entries []BuildLogEntry, nextStep int, ok bool) {
	if defaultTimeout <= 0 {
		defaultTimeout = 10 * time.Minute
	}

	step := startStep
	for _, st := range steps {
		step++
		slog.Info("executing build step", "phase", phase, "step", step, "instruction", st.Raw)

		switch st.Kind {
		case KindENV:
			if bctx.EnvVars == nil {
				bctx.EnvVars = make(map[string]string)
			}
			bctx.EnvVars[st.Key] = expandEnv(st.Value, bctx.EnvVars)
			entries = append(entries, BuildLogEntry{Step: step, Phase: phase, Cmd: st.Raw, Ok: true})

		case KindWORKDIR:
			bctx.WorkDir = st.Path
			entries = append(entries, BuildLogEntry{Step: step, Phase: phase, Cmd: st.Raw, Ok: true})

		case KindUSER, KindCOPY:
			verb := strings.ToUpper(strings.Fields(st.Raw)[0])
			entries = append(entries, BuildLogEntry{
				Step:   step,
				Phase:  phase,
				Cmd:    st.Raw,
				Stderr: verb + " is not yet supported",
				Ok:     false,
			})
			return entries, step, false

		case KindSTART:
			entry, succeeded := execStart(ctx, st, sandboxID, phase, step, bctx, execFn)
			entries = append(entries, entry)
			if !succeeded {
				return entries, step, false
			}

		case KindRUN:
			timeout := defaultTimeout
			if st.Timeout > 0 {
				timeout = st.Timeout
			}
			entry, succeeded := execRun(ctx, st, sandboxID, phase, step, timeout, bctx, execFn)
			entries = append(entries, entry)
			if !succeeded {
				return entries, step, false
			}
		}
	}
	return entries, step, true
}

func execRun(
	ctx context.Context,
	st Step,
	sandboxID, phase string,
	step int,
	timeout time.Duration,
	bctx *ExecContext,
	execFn ExecFunc,
) (BuildLogEntry, bool) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	resp, err := execFn(execCtx, connect.NewRequest(&pb.ExecRequest{
		SandboxId:  sandboxID,
		Cmd:        "/bin/sh",
		Args:       []string{"-c", bctx.WrappedCommand(st.Shell)},
		TimeoutSec: int32(timeout.Seconds()),
	}))

	entry := BuildLogEntry{
		Step:    step,
		Phase:   phase,
		Cmd:     st.Raw,
		Elapsed: time.Since(start).Milliseconds(),
	}
	if err != nil {
		entry.Stderr = fmt.Sprintf("exec error: %v", err)
		return entry, false
	}
	entry.Stdout = string(resp.Msg.Stdout)
	entry.Stderr = string(resp.Msg.Stderr)
	entry.Exit = resp.Msg.ExitCode
	entry.Ok = resp.Msg.ExitCode == 0
	return entry, entry.Ok
}

func execStart(
	ctx context.Context,
	st Step,
	sandboxID, phase string,
	step int,
	bctx *ExecContext,
	execFn ExecFunc,
) (BuildLogEntry, bool) {
	// START uses a short timeout: just long enough for the shell to fork and
	// return. The background process itself runs indefinitely inside the VM.
	execCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := execFn(execCtx, connect.NewRequest(&pb.ExecRequest{
		SandboxId:  sandboxID,
		Cmd:        "/bin/sh",
		Args:       []string{"-c", bctx.StartCommand(st.Shell)},
		TimeoutSec: 10,
	}))

	entry := BuildLogEntry{
		Step:    step,
		Phase:   phase,
		Cmd:     st.Raw,
		Elapsed: time.Since(start).Milliseconds(),
	}
	if err != nil {
		entry.Stderr = fmt.Sprintf("start error: %v", err)
		return entry, false
	}
	entry.Exit = resp.Msg.ExitCode
	entry.Ok = resp.Msg.ExitCode == 0
	if !entry.Ok {
		entry.Stderr = fmt.Sprintf("start failed with exit code %d: %s", resp.Msg.ExitCode, string(resp.Msg.Stderr))
	}
	return entry, entry.Ok
}
