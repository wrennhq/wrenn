package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
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

// ExecStream handles WS /v1/sandboxes/{id}/exec/stream.
func (h *execStreamHandler) ExecStream(w http.ResponseWriter, r *http.Request) {
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

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

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
		resp := stream.Msg()
		switch ev := resp.Event.(type) {
		case *pb.ExecStreamResponse_Start:
			writeWSJSON(conn, wsOutMsg{Type: "start", PID: ev.Start.Pid})

		case *pb.ExecStreamResponse_Data:
			switch o := ev.Data.Output.(type) {
			case *pb.ExecStreamData_Stdout:
				writeWSJSON(conn, wsOutMsg{Type: "stdout", Data: string(o.Stdout)})
			case *pb.ExecStreamData_Stderr:
				writeWSJSON(conn, wsOutMsg{Type: "stderr", Data: string(o.Stderr)})
			}

		case *pb.ExecStreamResponse_End:
			exitCode := ev.End.ExitCode
			writeWSJSON(conn, wsOutMsg{Type: "exit", ExitCode: &exitCode})
		}
	}

	if err := stream.Err(); err != nil {
		// Only send if the connection is still alive (not a normal close).
		if streamCtx.Err() == nil {
			sendWSError(conn, err.Error())
		}
	}

	// Update last active using a fresh context (the request context may be cancelled).
	updateCtx, updateCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer updateCancel()
	if err := h.db.UpdateLastActive(updateCtx, db.UpdateLastActiveParams{
		ID: sandboxID,
		LastActiveAt: pgtype.Timestamptz{
			Time:  time.Now(),
			Valid: true,
		},
	}); err != nil {
		slog.Warn("failed to update last active after stream exec", "sandbox_id", sandboxIDStr, "error", err)
	}
}

func sendWSError(conn *websocket.Conn, msg string) {
	writeWSJSON(conn, wsOutMsg{Type: "error", Data: msg})
}

func writeWSJSON(conn *websocket.Conn, v any) {
	if err := conn.WriteJSON(v); err != nil {
		slog.Debug("websocket write error", "error", err)
	}
}
