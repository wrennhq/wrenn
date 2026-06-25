package api

import (
	"context"
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
	ptyDefaultCols       = 80
	ptyDefaultRows       = 24
)

// ptyUpgrader enables permessage-deflate (RFC 7692) for the PTY WebSocket only.
// TUI output is highly compressible (repeated escape sequences, runs of spaces,
// repeated SGR color codes); the browser negotiates compression automatically.
// The other WebSocket endpoints keep the default uncompressed upgrader.
var ptyUpgrader = websocket.Upgrader{
	CheckOrigin:       func(r *http.Request) bool { return true },
	EnableCompression: true,
}

type ptyHandler struct {
	db   *db.Queries
	pool *lifecycle.HostClientPool
}

func newPtyHandler(db *db.Queries, pool *lifecycle.HostClientPool) *ptyHandler {
	return &ptyHandler{db: db, pool: pool}
}

// --- WebSocket message types ---

// wsPtyIn is an inbound message from the client. Control messages (start,
// connect, resize, kill) arrive as JSON text frames. Keystroke input arrives as
// raw binary frames and is represented here with Type "input" and the bytes in
// inputBytes (never JSON-encoded).
type wsPtyIn struct {
	Type string            `json:"type"`           // "start", "connect", "resize", "kill"
	Cmd  string            `json:"cmd,omitempty"`  // for "start"
	Args []string          `json:"args,omitempty"` // for "start"
	Cols uint32            `json:"cols,omitempty"` // for "start", "resize"
	Rows uint32            `json:"rows,omitempty"` // for "start", "resize"
	Envs map[string]string `json:"envs,omitempty"` // for "start"
	Cwd  string            `json:"cwd,omitempty"`  // for "start"
	User string            `json:"user,omitempty"` // for "start"
	Tag  string            `json:"tag,omitempty"`  // for "connect"

	inputBytes []byte // raw keystrokes from a binary frame (Type == "input")
}

// wsPtyOut is an outbound control message to the client (JSON text frame). PTY
// output is sent separately as raw binary frames, not through this struct.
type wsPtyOut struct {
	Type     string `json:"type"`                // "started", "exit", "error", "ping"
	Tag      string `json:"tag,omitempty"`       // for "started"
	PID      uint32 `json:"pid,omitempty"`       // for "started"
	Data     string `json:"data,omitempty"`      // for "error"
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

// writeBinary sends raw PTY output as a binary WebSocket frame. Binary avoids
// the ~33% base64 inflation of the JSON path; the negotiated permessage-deflate
// compression then runs on the raw bytes for maximum reduction.
func (w *wsWriter) writeBinary(data []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
		slog.Debug("pty websocket binary write error", "error", err)
	}
}

// PtySession handles WS /v1/capsules/{id}/pty.
func (h *ptyHandler) PtySession(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ctx := r.Context()

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	conn, ac, err := upgradeAndAuthenticateWith(w, r, &ptyUpgrader)
	if err != nil {
		slog.Error("pty websocket upgrade/auth failed", "error", err)
		return
	}
	defer conn.Close()

	ws := &wsWriter{conn: conn}
	h.runPtySession(ctx, ws, conn, ac, sandboxID, sandboxIDStr)
}

func (h *ptyHandler) runPtySession(ctx context.Context, ws *wsWriter, conn *websocket.Conn, ac auth.AuthContext, sandboxID pgtype.UUID, sandboxIDStr string) {
	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		ws.writeJSON(wsPtyOut{Type: "error", Data: "sandbox not found", Fatal: true})
		return
	}
	if sb.Status != "running" {
		ws.writeJSON(wsPtyOut{Type: "error", Data: "sandbox is not running (status: " + sb.Status + ")", Fatal: true})
		return
	}

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

	updateLastActive(h.db, sandboxID, sandboxIDStr)
}

func (h *ptyHandler) handleStart(
	ctx context.Context,
	cancel context.CancelFunc,
	ws *wsWriter,
	agent hostagentv1connect.HostAgentServiceClient,
	sandboxIDStr string,
	msg wsPtyIn,
) {
	// An empty cmd is intentional: envd launches the user's default login shell
	// (resolved from /etc/passwd) when no command is given.
	cmd := msg.Cmd
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
		Reconnect: true,
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
				ws.writeBinary(ev.Output.Data)

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

	// Input pump: decouple WebSocket reads from RPC dispatch.
	// Reader goroutine drains the WebSocket into a buffered channel;
	// sender goroutine dispatches RPCs at its own pace. This prevents
	// slow RPCs from stalling WebSocket reads and causing proxy timeouts.
	inputCh := make(chan wsPtyIn, 64)

	// Reader: drain WebSocket as fast as possible.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(inputCh)
		defer cancel()

		for {
			mt, raw, err := ws.conn.ReadMessage()
			if err != nil {
				return
			}

			var msg wsPtyIn
			if mt == websocket.BinaryMessage {
				// Raw keystrokes — forward bytes verbatim.
				msg = wsPtyIn{Type: "input", inputBytes: raw}
			} else if json.Unmarshal(raw, &msg) != nil {
				continue
			}

			select {
			case inputCh <- msg:
			default:
				// Buffer full — drop frame to keep reader unblocked.
				slog.Debug("pty input buffer full, dropping frame", "type", msg.Type)
			}
		}
	}()

	// Sender: dispatch RPCs from channel, coalescing consecutive input messages.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		// pending holds a non-input message dequeued during coalescing
		// that must be processed on the next iteration.
		var pending *wsPtyIn

		for {
			var msg wsPtyIn
			if pending != nil {
				msg = *pending
				pending = nil
			} else {
				var ok bool
				msg, ok = <-inputCh
				if !ok {
					break
				}
			}

			rpcCtx, rpcCancel := context.WithTimeout(context.Background(), 5*time.Second)

			switch msg.Type {
			case "input":
				// Coalesce: drain any queued input messages into a single RPC.
				var data []byte
				data, pending = coalescePtyInput(inputCh, msg.inputBytes)
				if len(data) == 0 {
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

	// When any pump cancels the context, close the websocket to unblock
	// the reader goroutine stuck in ReadMessage.
	go func() {
		<-ctx.Done()
		ws.conn.Close()
	}()

	wg.Wait()
}

// coalescePtyInput drains any immediately-available "input" messages from the
// channel and appends their decoded data to buf, reducing RPC call volume
// during bursts of fast typing. Returns the coalesced buffer and any
// non-input message that was dequeued (must be processed by the caller).
func coalescePtyInput(ch <-chan wsPtyIn, buf []byte) ([]byte, *wsPtyIn) {
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return buf, nil
			}
			if msg.Type != "input" {
				return buf, &msg
			}
			buf = append(buf, msg.inputBytes...)
		default:
			return buf, nil
		}
	}
}

// newPtyTag returns a PTY session tag: "pty-" + 8 random hex chars.
func newPtyTag() string {
	return "pty-" + id.NewPtyTag()
}
