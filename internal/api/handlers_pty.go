package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
	"git.omukk.dev/wrenn/wrenn/proto/hostagent/gen/hostagentv1connect"
)

const (
	ptyKeepaliveInterval = 30 * time.Second
	ptyDefaultCmd        = "/bin/bash"
	ptyDefaultCols       = 80
	ptyDefaultRows       = 24
)

type ptyHandler struct {
	db   *db.Queries
	pool *lifecycle.HostClientPool
}

func newPtyHandler(db *db.Queries, pool *lifecycle.HostClientPool) *ptyHandler {
	return &ptyHandler{db: db, pool: pool}
}

// --- WebSocket message types ---

// wsPtyIn is the inbound message from the client.
type wsPtyIn struct {
	Type string            `json:"type"`           // "start", "connect", "input", "resize", "kill"
	Cmd  string            `json:"cmd,omitempty"`  // for "start"
	Args []string          `json:"args,omitempty"` // for "start"
	Cols uint32            `json:"cols,omitempty"` // for "start", "resize"
	Rows uint32            `json:"rows,omitempty"` // for "start", "resize"
	Envs map[string]string `json:"envs,omitempty"` // for "start"
	Cwd  string            `json:"cwd,omitempty"`  // for "start"
	User string            `json:"user,omitempty"` // for "start"
	Tag  string            `json:"tag,omitempty"`  // for "connect"
	Data string            `json:"data,omitempty"` // for "input" (base64)
}

// wsPtyOut is the outbound message to the client.
type wsPtyOut struct {
	Type     string `json:"type"`                // "started", "output", "exit", "error"
	Tag      string `json:"tag,omitempty"`       // for "started"
	PID      uint32 `json:"pid,omitempty"`       // for "started"
	Data     string `json:"data,omitempty"`      // for "output" (base64), "error"
	ExitCode *int32 `json:"exit_code,omitempty"` // for "exit"
	Fatal    bool   `json:"fatal,omitempty"`     // for "error"
}

// wsWriter wraps a websocket.Conn with a mutex for concurrent writes.
type wsWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsWriter) writeJSON(v any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.conn.WriteJSON(v); err != nil {
		slog.Debug("pty websocket write error", "error", err)
	}
}

// PtySession handles WS /v1/capsules/{id}/pty.
func (h *ptyHandler) PtySession(w http.ResponseWriter, r *http.Request) {
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
		slog.Error("pty websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	ws := &wsWriter{conn: conn}

	// Read the first message to determine start vs connect.
	var firstMsg wsPtyIn
	if err := conn.ReadJSON(&firstMsg); err != nil {
		ws.writeJSON(wsPtyOut{Type: "error", Data: "failed to read first message: " + err.Error(), Fatal: true})
		return
	}

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		ws.writeJSON(wsPtyOut{Type: "error", Data: "sandbox host is not reachable", Fatal: true})
		return
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	switch firstMsg.Type {
	case "start":
		h.handleStart(streamCtx, cancel, ws, agent, sandboxIDStr, firstMsg)
	case "connect":
		h.handleConnect(streamCtx, cancel, ws, agent, sandboxIDStr, firstMsg)
	default:
		ws.writeJSON(wsPtyOut{Type: "error", Data: "first message must be type 'start' or 'connect'", Fatal: true})
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
		slog.Warn("failed to update last active after pty session", "sandbox_id", sandboxIDStr, "error", err)
	}
}

func (h *ptyHandler) handleStart(
	ctx context.Context,
	cancel context.CancelFunc,
	ws *wsWriter,
	agent hostagentv1connect.HostAgentServiceClient,
	sandboxIDStr string,
	msg wsPtyIn,
) {
	cmd := msg.Cmd
	if cmd == "" {
		cmd = ptyDefaultCmd
	}
	cols := msg.Cols
	if cols == 0 {
		cols = ptyDefaultCols
	}
	rows := msg.Rows
	if rows == 0 {
		rows = ptyDefaultRows
	}

	tag := newPtyTag()

	stream, err := agent.PtyAttach(ctx, connect.NewRequest(&pb.PtyAttachRequest{
		SandboxId: sandboxIDStr,
		Tag:       tag,
		Cmd:       cmd,
		Args:      msg.Args,
		Cols:      cols,
		Rows:      rows,
		Envs:      msg.Envs,
		Cwd:       msg.Cwd,
		User:      msg.User,
	}))
	if err != nil {
		ws.writeJSON(wsPtyOut{Type: "error", Data: "failed to start pty: " + err.Error(), Fatal: true})
		return
	}
	defer stream.Close()

	// Wait for the started event and forward it.
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			ws.writeJSON(wsPtyOut{Type: "error", Data: "pty stream failed: " + err.Error(), Fatal: true})
		}
		return
	}
	resp := stream.Msg()
	started, ok := resp.Event.(*pb.PtyAttachResponse_Started)
	if !ok {
		ws.writeJSON(wsPtyOut{Type: "error", Data: "expected started event from host agent", Fatal: true})
		return
	}
	ws.writeJSON(wsPtyOut{Type: "started", Tag: started.Started.Tag, PID: started.Started.Pid})

	runPtyLoop(ctx, cancel, ws, stream, agent, sandboxIDStr, tag)
}

func (h *ptyHandler) handleConnect(
	ctx context.Context,
	cancel context.CancelFunc,
	ws *wsWriter,
	agent hostagentv1connect.HostAgentServiceClient,
	sandboxIDStr string,
	msg wsPtyIn,
) {
	if msg.Tag == "" {
		ws.writeJSON(wsPtyOut{Type: "error", Data: "connect requires a 'tag' field", Fatal: true})
		return
	}

	stream, err := agent.PtyAttach(ctx, connect.NewRequest(&pb.PtyAttachRequest{
		SandboxId: sandboxIDStr,
		Tag:       msg.Tag,
	}))
	if err != nil {
		ws.writeJSON(wsPtyOut{Type: "error", Data: "failed to connect to pty: " + err.Error(), Fatal: true})
		return
	}
	defer stream.Close()

	runPtyLoop(ctx, cancel, ws, stream, agent, sandboxIDStr, msg.Tag)
}

// runPtyLoop drives the bidirectional communication between the WebSocket
// and the host agent PTY stream.
func runPtyLoop(
	ctx context.Context,
	cancel context.CancelFunc,
	ws *wsWriter,
	stream *connect.ServerStreamForClient[pb.PtyAttachResponse],
	agent hostagentv1connect.HostAgentServiceClient,
	sandboxID string,
	tag string,
) {
	var wg sync.WaitGroup

	// Output pump: read from Connect stream, write to WebSocket.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		for stream.Receive() {
			resp := stream.Msg()
			switch ev := resp.Event.(type) {
			case *pb.PtyAttachResponse_Started:
				// Already handled before the loop for "start" mode.
				// For "connect" mode this won't appear.
				ws.writeJSON(wsPtyOut{Type: "started", Tag: ev.Started.Tag, PID: ev.Started.Pid})

			case *pb.PtyAttachResponse_Output:
				ws.writeJSON(wsPtyOut{
					Type: "output",
					Data: base64.StdEncoding.EncodeToString(ev.Output.Data),
				})

			case *pb.PtyAttachResponse_Exited:
				exitCode := ev.Exited.ExitCode
				ws.writeJSON(wsPtyOut{Type: "exit", ExitCode: &exitCode})
				return
			}
		}

		if err := stream.Err(); err != nil && ctx.Err() == nil {
			ws.writeJSON(wsPtyOut{Type: "error", Data: err.Error()})
		}
	}()

	// Input pump: read from WebSocket, dispatch to host agent.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		for {
			_, raw, err := ws.conn.ReadMessage()
			if err != nil {
				return
			}

			var msg wsPtyIn
			if json.Unmarshal(raw, &msg) != nil {
				continue
			}

			// Use a background context for unary RPCs so they complete
			// even if the stream context is being cancelled.
			rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 5*time.Second)

			switch msg.Type {
			case "input":
				data, err := base64.StdEncoding.DecodeString(msg.Data)
				if err != nil {
					rpcCancel()
					continue
				}
				if _, err := agent.PtySendInput(rpcCtx, connect.NewRequest(&pb.PtySendInputRequest{
					SandboxId: sandboxID,
					Tag:       tag,
					Data:      data,
				})); err != nil {
					slog.Debug("pty send input error", "error", err)
				}

			case "resize":
				cols := msg.Cols
				rows := msg.Rows
				if cols > 0 && rows > 0 {
					if _, err := agent.PtyResize(rpcCtx, connect.NewRequest(&pb.PtyResizeRequest{
						SandboxId: sandboxID,
						Tag:       tag,
						Cols:      cols,
						Rows:      rows,
					})); err != nil {
						slog.Debug("pty resize error", "error", err)
					}
				}

			case "kill":
				if _, err := agent.PtyKill(rpcCtx, connect.NewRequest(&pb.PtyKillRequest{
					SandboxId: sandboxID,
					Tag:       tag,
				})); err != nil {
					slog.Debug("pty kill error", "error", err)
				}
			}

			rpcCancel()
		}
	}()

	// Keepalive pump: send periodic pings to prevent idle WS closure.
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(ptyKeepaliveInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				ws.writeJSON(wsPtyOut{Type: "ping"})
			case <-ctx.Done():
				return
			}
		}
	}()

	wg.Wait()
}

// newPtyTag returns a PTY session tag: "pty-" + 8 random hex chars.
func newPtyTag() string {
	return "pty-" + id.NewPtyTag()
}
