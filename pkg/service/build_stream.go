package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// buildStreamChannelPrefix is the Redis pub/sub channel prefix for live build
// events. One channel per build: wrenn:build:{buildID}.
const buildStreamChannelPrefix = "wrenn:build:"

func buildStreamChannel(buildID string) string {
	return buildStreamChannelPrefix + buildID
}

// BuildStreamEvent is one event in a build's live stream. The same struct is
// published to Redis by the build worker and forwarded verbatim to admin
// WebSocket clients, so its JSON shape is the wire contract for both.
//
// Type discriminates the payload:
//   - "step-start":   Step, Phase, Cmd set.
//   - "output":       Step, Data (base64 PTY bytes) set.
//   - "step-end":     Step, Phase, Cmd, Exit, Ok, ElapsedMs set.
//   - "build-status": Status, CurrentStep, TotalSteps, Error set.
type BuildStreamEvent struct {
	Type        string `json:"type"`
	Step        int    `json:"step,omitempty"`
	Phase       string `json:"phase,omitempty"`
	Cmd         string `json:"cmd,omitempty"`
	Data        string `json:"data,omitempty"` // base64-encoded PTY output bytes
	Exit        int32  `json:"exit,omitempty"`
	Ok          bool   `json:"ok,omitempty"`
	ElapsedMs   int64  `json:"elapsed_ms,omitempty"`
	Status      string `json:"status,omitempty"`
	CurrentStep int32  `json:"current_step,omitempty"`
	TotalSteps  int32  `json:"total_steps,omitempty"`
	Error       string `json:"error,omitempty"`
	T           int64  `json:"t"` // unix milliseconds, set at publish time
}

// IsTerminalBuildStatus reports whether a build status is final (the worker
// will publish no further events for it).
func IsTerminalBuildStatus(status string) bool {
	switch status {
	case "success", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// publishBuildEvent fire-and-forget publishes one event to a build's Redis
// channel. A missing/closed Redis connection only drops live events; the WS
// client always has the DB log history to fall back on.
func publishBuildEvent(ctx context.Context, rdb *redis.Client, buildID string, ev BuildStreamEvent) {
	if rdb == nil {
		return
	}
	ev.T = time.Now().UnixMilli()
	payload, err := json.Marshal(ev)
	if err != nil {
		slog.Warn("build event marshal failed", "build_id", buildID, "error", err)
		return
	}
	if err := rdb.Publish(ctx, buildStreamChannel(buildID), payload).Err(); err != nil {
		slog.Debug("build event publish failed", "build_id", buildID, "error", err)
	}
}
