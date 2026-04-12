package api

import (
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/auth"
	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/id"
	"git.omukk.dev/wrenn/wrenn/internal/lifecycle"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

type execHandler struct {
	db   *db.Queries
	pool *lifecycle.HostClientPool
}

func newExecHandler(db *db.Queries, pool *lifecycle.HostClientPool) *execHandler {
	return &execHandler{db: db, pool: pool}
}

type execRequest struct {
	Cmd        string   `json:"cmd"`
	Args       []string `json:"args"`
	TimeoutSec int32    `json:"timeout_sec"`
}

type execResponse struct {
	SandboxID  string `json:"sandbox_id"`
	Cmd        string `json:"cmd"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int32  `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	// Encoding is "utf-8" for text output, "base64" for binary output.
	Encoding string `json:"encoding"`
}

// Exec handles POST /v1/capsules/{id}/exec.
func (h *execHandler) Exec(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sb.Status != "running" {
		writeError(w, http.StatusConflict, "invalid_state", "sandbox is not running (status: "+sb.Status+")")
		return
	}

	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if req.Cmd == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "cmd is required")
		return
	}

	start := time.Now()

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host_unavailable", "sandbox host is not reachable")
		return
	}

	resp, err := agent.Exec(ctx, connect.NewRequest(&pb.ExecRequest{
		SandboxId:  sandboxIDStr,
		Cmd:        req.Cmd,
		Args:       req.Args,
		TimeoutSec: req.TimeoutSec,
	}))
	if err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	duration := time.Since(start)

	// Update last active.
	if err := h.db.UpdateLastActive(ctx, db.UpdateLastActiveParams{
		ID: sandboxID,
		LastActiveAt: pgtype.Timestamptz{
			Time:  time.Now(),
			Valid: true,
		},
	}); err != nil {
		slog.Warn("failed to update last_active_at", "id", sandboxIDStr, "error", err)
	}

	// Use base64 encoding if output contains non-UTF-8 bytes.
	stdout := resp.Msg.Stdout
	stderr := resp.Msg.Stderr
	encoding := "utf-8"

	if !utf8.Valid(stdout) || !utf8.Valid(stderr) {
		encoding = "base64"
		writeJSON(w, http.StatusOK, execResponse{
			SandboxID:  sandboxIDStr,
			Cmd:        req.Cmd,
			Stdout:     base64.StdEncoding.EncodeToString(stdout),
			Stderr:     base64.StdEncoding.EncodeToString(stderr),
			ExitCode:   resp.Msg.ExitCode,
			DurationMs: duration.Milliseconds(),
			Encoding:   encoding,
		})
		return
	}

	writeJSON(w, http.StatusOK, execResponse{
		SandboxID:  sandboxIDStr,
		Cmd:        req.Cmd,
		Stdout:     string(stdout),
		Stderr:     string(stderr),
		ExitCode:   resp.Msg.ExitCode,
		DurationMs: duration.Milliseconds(),
		Encoding:   encoding,
	})
}
