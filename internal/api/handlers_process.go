package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/auth"
	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/id"
	"git.omukk.dev/wrenn/wrenn/internal/lifecycle"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

type processHandler struct {
	db   *db.Queries
	pool *lifecycle.HostClientPool
}

func newProcessHandler(db *db.Queries, pool *lifecycle.HostClientPool) *processHandler {
	return &processHandler{db: db, pool: pool}
}

// processResponse is a single entry in the process list.
type processResponse struct {
	PID  uint32   `json:"pid"`
	Tag  string   `json:"tag,omitempty"`
	Cmd  string   `json:"cmd"`
	Args []string `json:"args,omitempty"`
}

// processListResponse wraps the list of processes.
type processListResponse struct {
	Processes []processResponse `json:"processes"`
}

// ListProcesses handles GET /v1/capsules/{id}/processes.
func (h *processHandler) ListProcesses(w http.ResponseWriter, r *http.Request) {
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

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host_unavailable", "sandbox host is not reachable")
		return
	}

	resp, err := agent.ListProcesses(ctx, connect.NewRequest(&pb.ListProcessesRequest{
		SandboxId: sandboxIDStr,
	}))
	if err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	procs := make([]processResponse, 0, len(resp.Msg.Processes))
	for _, p := range resp.Msg.Processes {
		procs = append(procs, processResponse{
			PID:  p.Pid,
			Tag:  p.Tag,
			Cmd:  p.Cmd,
			Args: p.Args,
		})
	}

	writeJSON(w, http.StatusOK, processListResponse{Processes: procs})
}

// KillProcess handles DELETE /v1/capsules/{id}/processes/{selector}.
// The selector can be a numeric PID or a string tag.
func (h *processHandler) KillProcess(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	selectorStr := chi.URLParam(r, "selector")
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

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host_unavailable", "sandbox host is not reachable")
		return
	}

	// Build the kill request with PID or tag selector.
	killReq := &pb.KillProcessRequest{
		SandboxId: sandboxIDStr,
		Signal:    "SIGKILL",
	}
	if sig := r.URL.Query().Get("signal"); sig == "SIGTERM" {
		killReq.Signal = "SIGTERM"
	}

	if pid, err := strconv.ParseUint(selectorStr, 10, 32); err == nil {
		killReq.Selector = &pb.KillProcessRequest_Pid{Pid: uint32(pid)}
	} else {
		killReq.Selector = &pb.KillProcessRequest_Tag{Tag: selectorStr}
	}

	if _, err := agent.KillProcess(ctx, connect.NewRequest(killReq)); err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// wsProcessOut is the JSON message sent to the WebSocket client.
type wsProcessOut struct {
	Type     string `json:"type"`                // "start", "stdout", "stderr", "exit", "error"
	PID      uint32 `json:"pid,omitempty"`       // only for "start"
	Data     string `json:"data,omitempty"`      // only for "stdout", "stderr", "error"
	ExitCode *int32 `json:"exit_code,omitempty"` // only for "exit"
}

// ConnectProcess handles WS /v1/capsules/{id}/processes/{selector}/stream.
func (h *processHandler) ConnectProcess(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	selectorStr := chi.URLParam(r, "selector")
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

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host_unavailable", "sandbox host is not reachable")
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("process stream websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	// Build the connect request with PID or tag selector.
	connectReq := &pb.ConnectProcessRequest{
		SandboxId: sandboxIDStr,
	}
	if pid, err := strconv.ParseUint(selectorStr, 10, 32); err == nil {
		connectReq.Selector = &pb.ConnectProcessRequest_Pid{Pid: uint32(pid)}
	} else {
		connectReq.Selector = &pb.ConnectProcessRequest_Tag{Tag: selectorStr}
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := agent.ConnectProcess(streamCtx, connect.NewRequest(connectReq))
	if err != nil {
		sendProcessWSError(conn, "failed to connect to process: "+err.Error())
		return
	}
	defer stream.Close()

	// Listen for client disconnect in a goroutine.
	go func() {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
		}
	}()

	// Forward stream events to WebSocket.
	for stream.Receive() {
		resp := stream.Msg()
		switch ev := resp.Event.(type) {
		case *pb.ConnectProcessResponse_Start:
			writeWSJSON(conn, wsProcessOut{Type: "start", PID: ev.Start.Pid})

		case *pb.ConnectProcessResponse_Data:
			switch o := ev.Data.Output.(type) {
			case *pb.ExecStreamData_Stdout:
				writeWSJSON(conn, wsProcessOut{Type: "stdout", Data: string(o.Stdout)})
			case *pb.ExecStreamData_Stderr:
				writeWSJSON(conn, wsProcessOut{Type: "stderr", Data: string(o.Stderr)})
			}

		case *pb.ConnectProcessResponse_End:
			exitCode := ev.End.ExitCode
			writeWSJSON(conn, wsProcessOut{Type: "exit", ExitCode: &exitCode})
		}
	}

	if err := stream.Err(); err != nil {
		if streamCtx.Err() == nil {
			sendProcessWSError(conn, err.Error())
		}
	}

	// Update last active using a fresh context.
	updateCtx, updateCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer updateCancel()
	if err := h.db.UpdateLastActive(updateCtx, db.UpdateLastActiveParams{
		ID: sandboxID,
		LastActiveAt: pgtype.Timestamptz{
			Time:  time.Now(),
			Valid: true,
		},
	}); err != nil {
		slog.Warn("failed to update last active after process stream", "sandbox_id", sandboxIDStr, "error", err)
	}
}

func sendProcessWSError(conn *websocket.Conn, msg string) {
	writeWSJSON(conn, wsProcessOut{Type: "error", Data: msg})
}
