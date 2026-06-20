package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

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

type execStreamHandler struct {
	db   *db.Queries
	pool *lifecycle.HostClientPool
}

func newExecStreamHandler(db *db.Queries, pool *lifecycle.HostClientPool) *execStreamHandler {
	return &execStreamHandler{db: db, pool: pool}
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsStartMsg is the first message the client sends to start a process.
type wsStartMsg struct {
	Type string   `json:"type"` // "start"
	Cmd  string   `json:"cmd"`
	Args []string `json:"args"`
}

// wsOutMsg is sent by the server for process events.
type wsOutMsg struct {
	Type     string `json:"type"`                // "start", "stdout", "stderr", "exit", "error"
	PID      uint32 `json:"pid,omitempty"`       // only for "start"
	Data     string `json:"data,omitempty"`      // only for "stdout", "stderr", "error"
	ExitCode *int32 `json:"exit_code,omitempty"` // only for "exit"
}

// ExecStream handles WS /v1/capsules/{id}/exec/stream.
func (h *execStreamHandler) ExecStream(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ctx := r.Context()

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	conn, ac, err := upgradeAndAuthenticate(w, r)
	if err != nil {
		slog.Error("websocket upgrade/auth failed", "error", err)
		return
	}
	defer conn.Close()

	h.runExecStream(ctx, conn, ac, sandboxID, sandboxIDStr)
}

func (h *execStreamHandler) runExecStream(ctx context.Context, conn *websocket.Conn, ac auth.AuthContext, sandboxID pgtype.UUID, sandboxIDStr string) {
	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		sendWSError(conn, "sandbox not found")
		return
	}
	if sb.Status != "running" {
		sendWSError(conn, "sandbox is not running (status: "+sb.Status+")")
		return
	}

	// Read the start message.
	var startMsg wsStartMsg
	if err := conn.ReadJSON(&startMsg); err != nil {
		sendWSError(conn, "failed to read start message: "+err.Error())
		return
	}
	if startMsg.Type != "start" || startMsg.Cmd == "" {
		sendWSError(conn, "first message must be type 'start' with a 'cmd' field")
		return
	}

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		sendWSError(conn, "sandbox host is not reachable")
		return
	}

	// Open streaming exec to host agent.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := agent.ExecStream(streamCtx, connect.NewRequest(&pb.ExecStreamRequest{
		SandboxId: sandboxIDStr,
		Cmd:       startMsg.Cmd,
		Args:      startMsg.Args,
	}))
	if err != nil {
		sendWSError(conn, "failed to start exec stream: "+err.Error())
		return
	}
	defer stream.Close()

	// Listen for stop messages from the client in a goroutine.
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
			var parsed struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(msg, &parsed) == nil && parsed.Type == "stop" {
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
		// Only send if the connection is still alive (not a normal close).
		if streamCtx.Err() == nil {
			sendWSError(conn, err.Error())
		}
	}

	updateLastActive(h.db, sandboxID, sandboxIDStr)
}

// procStreamResp is satisfied by both *pb.ExecStreamResponse and
// *pb.ConnectProcessResponse: their oneof events carry the same inner messages,
// so the wire-to-WS mapping below is shared between the exec-stream and
// connect-process handlers.
type procStreamResp interface {
	GetStart() *pb.ExecStreamStart
	GetData() *pb.ExecStreamData
	GetEnd() *pb.ExecStreamEnd
}

// procRespToWSMsg maps one process stream response to the WS message to send.
// The bool is false when the response carries nothing to forward.
func procRespToWSMsg(resp procStreamResp) (wsOutMsg, bool) {
	if s := resp.GetStart(); s != nil {
		return wsOutMsg{Type: "start", PID: s.Pid}, true
	}
	if d := resp.GetData(); d != nil {
		switch o := d.Output.(type) {
		case *pb.ExecStreamData_Stdout:
			return wsOutMsg{Type: "stdout", Data: string(o.Stdout)}, true
		case *pb.ExecStreamData_Stderr:
			return wsOutMsg{Type: "stderr", Data: string(o.Stderr)}, true
		}
		return wsOutMsg{}, false
	}
	if e := resp.GetEnd(); e != nil {
		exitCode := e.ExitCode
		return wsOutMsg{Type: "exit", ExitCode: &exitCode}, true
	}
	return wsOutMsg{}, false
}

func sendWSError(conn *websocket.Conn, msg string) {
	writeWSJSON(conn, wsOutMsg{Type: "error", Data: msg})
}

func writeWSJSON(conn *websocket.Conn, v any) {
	if err := conn.WriteJSON(v); err != nil {
		slog.Debug("websocket write error", "error", err)
	}
}
