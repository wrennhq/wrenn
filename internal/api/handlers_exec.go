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

	"git.omukk.dev/wrenn/sandbox/internal/db"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
)

type execHandler struct {
	db    *db.Queries
	agent hostagentv1connect.HostAgentServiceClient
}

func newExecHandler(db *db.Queries, agent hostagentv1connect.HostAgentServiceClient) *execHandler {
	return &execHandler{db: db, agent: agent}
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

// Exec handles POST /v1/sandboxes/{id}/exec.
func (h *execHandler) Exec(w http.ResponseWriter, r *http.Request) {
	sandboxID := chi.URLParam(r, "id")
	ctx := r.Context()

	sb, err := h.db.GetSandbox(ctx, sandboxID)
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

	resp, err := h.agent.Exec(ctx, connect.NewRequest(&pb.ExecRequest{
		SandboxId:  sandboxID,
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
		slog.Warn("failed to update last_active_at", "id", sandboxID, "error", err)
	}

	// Use base64 encoding if output contains non-UTF-8 bytes.
	stdout := resp.Msg.Stdout
	stderr := resp.Msg.Stderr
	encoding := "utf-8"

	if !utf8.Valid(stdout) || !utf8.Valid(stderr) {
		encoding = "base64"
		writeJSON(w, http.StatusOK, execResponse{
			SandboxID:  sandboxID,
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
		SandboxID:  sandboxID,
		Cmd:        req.Cmd,
		Stdout:     string(stdout),
		Stderr:     string(stderr),
		ExitCode:   resp.Msg.ExitCode,
		DurationMs: duration.Milliseconds(),
		Encoding:   encoding,
	})
}
