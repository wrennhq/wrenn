package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"git.omukk.dev/wrenn/wrenn/internal/recipe"
	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
)

// buildStreamKeepalive is the interval between server pings on an idle build
// stream, preventing intermediaries from closing the WebSocket.
const buildStreamKeepalive = 30 * time.Second

// buildStreamHandler serves the live admin build console WebSocket.
type buildStreamHandler struct {
	db     *db.Queries
	broker *service.BuildBroker
}

func newBuildStreamHandler(db *db.Queries, broker *service.BuildBroker) *buildStreamHandler {
	return &buildStreamHandler{db: db, broker: broker}
}

// Stream handles WS /v1/admin/builds/{id}/stream. On connect it replays the
// completed-step history from the DB log, sends the current build status,
// then live-tails events from the build broker until the build finishes or
// the client disconnects. Admin auth is enforced by upstream middleware.
func (h *buildStreamHandler) Stream(w http.ResponseWriter, r *http.Request) {
	buildIDStr := chi.URLParam(r, "id")
	buildID, err := id.ParseBuildID(buildIDStr)
	if err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid build ID."))
		return
	}

	build, err := h.db.GetTemplateBuild(r.Context(), buildID)
	if err != nil {
		writeErr(w, r, apperr.BuildNotFound.Wrap(err))
		return
	}

	conn, _, err := upgradeAndAuthenticate(w, r)
	if err != nil {
		slog.Error("build stream websocket upgrade/auth failed", "error", err)
		return
	}
	defer conn.Close()

	h.runStream(r.Context(), conn, build)
}

func (h *buildStreamHandler) runStream(ctx context.Context, conn *websocket.Conn, build db.TemplateBuild) {
	ws := &wsWriter{conn: conn}
	buildIDStr := id.FormatBuildID(build.ID)

	// Replay completed-step history from the DB log snapshot. lastStep is the
	// highest step number already delivered, used to dedup overlapping live
	// events for a step that finished between the DB read and the subscribe.
	lastStep := replayBuildHistory(ws, build)

	ws.writeJSON(service.BuildStreamEvent{
		Type:        "build-status",
		Status:      build.Status,
		CurrentStep: build.CurrentStep,
		TotalSteps:  build.TotalSteps,
		Error:       build.Error,
	})

	// A finished build has no live events to follow.
	if service.IsTerminalBuildStatus(build.Status) {
		return
	}

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	events, release := h.broker.Subscribe(buildIDStr)
	defer release()

	// Drain client reads so a disconnect cancels the stream. The client sends
	// nothing meaningful; any read error means the socket is gone.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cancel()
				return
			}
		}
	}()

	ticker := time.NewTicker(buildStreamKeepalive)
	defer ticker.Stop()

	for {
		select {
		case <-streamCtx.Done():
			return
		case <-ticker.C:
			ws.writeJSON(map[string]string{"type": "ping"})
		case ev, ok := <-events:
			if !ok {
				return
			}
			// Skip step events already covered by the history replay.
			if ev.Type != "build-status" && ev.Step > 0 && ev.Step <= lastStep {
				continue
			}
			ws.writeJSON(ev)
			if ev.Type == "build-status" && service.IsTerminalBuildStatus(ev.Status) {
				return
			}
		}
	}
}

// replayBuildHistory synthesizes step-start/output/step-end events from the
// build's persisted log entries and writes them to the WebSocket. It returns
// the highest step number replayed.
func replayBuildHistory(ws *wsWriter, build db.TemplateBuild) int {
	if len(build.Logs) == 0 {
		return 0
	}
	var entries []recipe.BuildLogEntry
	if err := json.Unmarshal(build.Logs, &entries); err != nil {
		slog.Warn("build stream: bad log JSON", "build_id", id.FormatBuildID(build.ID), "error", err)
		return 0
	}

	lastStep := 0
	for _, e := range entries {
		ws.writeJSON(service.BuildStreamEvent{Type: "step-start", Step: e.Step, Phase: e.Phase, Cmd: e.Cmd})
		if out := e.Stdout + e.Stderr; out != "" {
			ws.writeJSON(service.BuildStreamEvent{
				Type: "output",
				Step: e.Step,
				Data: base64.StdEncoding.EncodeToString([]byte(out)),
			})
		}
		ws.writeJSON(service.BuildStreamEvent{
			Type: "step-end", Step: e.Step, Phase: e.Phase, Cmd: e.Cmd,
			Exit: e.Exit, Ok: e.Ok, ElapsedMs: e.Elapsed,
		})
		if e.Step > lastStep {
			lastStep = e.Step
		}
	}
	return lastStep
}
