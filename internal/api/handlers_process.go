package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
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
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, _, sandboxIDStr, ok := requireRunningSandbox(w, r, h.db, ac.TeamID)
	if !ok {
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
	selectorStr := chi.URLParam(r, "selector")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, _, sandboxIDStr, ok := requireRunningSandbox(w, r, h.db, ac.TeamID)
	if !ok {
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

// ConnectProcess handles WS /v1/capsules/{id}/processes/{selector}/stream.
func (h *processHandler) ConnectProcess(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	selectorStr := chi.URLParam(r, "selector")
	ctx := r.Context()

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	conn, ac, err := upgradeAndAuthenticate(w, r)
	if err != nil {
		slog.Error("process stream websocket upgrade/auth failed", "error", err)
		return
	}
	defer conn.Close()

	h.runConnectProcess(ctx, conn, ac, sandboxID, sandboxIDStr, selectorStr)
}

func (h *processHandler) runConnectProcess(ctx context.Context, conn *websocket.Conn, ac auth.AuthContext, sandboxID pgtype.UUID, sandboxIDStr, selectorStr string) {
	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		sendWSError(conn, "sandbox not found")
		return
	}
	if sb.Status != "running" {
		sendWSError(conn, "sandbox is not running (status: "+sb.Status+")")
		return
	}

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		sendWSError(conn, "sandbox host is not reachable")
		return
	}

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
		sendWSError(conn, "failed to connect to process: "+err.Error())
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
		if m, ok := procRespToWSMsg(stream.Msg()); ok {
			writeWSJSON(conn, m)
		}
	}

	if err := stream.Err(); err != nil {
		if streamCtx.Err() == nil {
			sendWSError(conn, err.Error())
		}
	}

	updateLastActive(h.db, sandboxID, sandboxIDStr)
}
