package api

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
)

type sandboxMetricsHandler struct {
	db   *db.Queries
	pool *lifecycle.HostClientPool
}

func newSandboxMetricsHandler(db *db.Queries, pool *lifecycle.HostClientPool) *sandboxMetricsHandler {
	return &sandboxMetricsHandler{db: db, pool: pool}
}

type metricPointResponse struct {
	TimestampUnix int64   `json:"timestamp_unix"`
	CPUPct        float64 `json:"cpu_pct"`
	MemBytes      int64   `json:"mem_bytes"`
	DiskBytes     int64   `json:"disk_bytes"`
}

type metricsResponse struct {
	SandboxID string                `json:"sandbox_id"`
	Range     string                `json:"range"`
	Points    []metricPointResponse `json:"points"`
}

// GetMetrics handles GET /v1/sandboxes/{id}/metrics?range=10m|2h|24h.
func (h *sandboxMetricsHandler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	sandboxID := chi.URLParam(r, "id")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	rangeTier := r.URL.Query().Get("range")
	if rangeTier == "" {
		rangeTier = "10m"
	}
	if rangeTier != "10m" && rangeTier != "2h" && rangeTier != "24h" {
		writeError(w, http.StatusBadRequest, "invalid_request", "range must be 10m, 2h, or 24h")
		return
	}

	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	switch sb.Status {
	case "running":
		h.getFromAgent(w, r, sandboxID, rangeTier, sb.HostID)
	case "paused":
		h.getFromDB(ctx, w, sandboxID, rangeTier)
	default:
		writeError(w, http.StatusNotFound, "not_found", "metrics not available for sandbox in state: "+sb.Status)
	}
}

func (h *sandboxMetricsHandler) getFromAgent(w http.ResponseWriter, r *http.Request, sandboxID, rangeTier, hostID string) {
	ctx := r.Context()

	agent, err := agentForHost(ctx, h.db, h.pool, hostID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host_unavailable", "sandbox host is not reachable")
		return
	}

	resp, err := agent.GetSandboxMetrics(ctx, connect.NewRequest(&pb.GetSandboxMetricsRequest{
		SandboxId: sandboxID,
		Range:     rangeTier,
	}))
	if err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	points := make([]metricPointResponse, len(resp.Msg.Points))
	for i, p := range resp.Msg.Points {
		points[i] = metricPointResponse{
			TimestampUnix: p.TimestampUnix,
			CPUPct:        p.CpuPct,
			MemBytes:      p.MemBytes,
			DiskBytes:     p.DiskBytes,
		}
	}

	writeJSON(w, http.StatusOK, metricsResponse{
		SandboxID: sandboxID,
		Range:     rangeTier,
		Points:    points,
	})
}

func (h *sandboxMetricsHandler) getFromDB(ctx context.Context, w http.ResponseWriter, sandboxID, rangeTier string) {
	rows, err := h.db.GetSandboxMetricPoints(ctx, db.GetSandboxMetricPointsParams{
		SandboxID: sandboxID,
		Tier:      rangeTier,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to read metrics")
		return
	}

	points := make([]metricPointResponse, len(rows))
	for i, row := range rows {
		points[i] = metricPointResponse{
			TimestampUnix: row.Ts,
			CPUPct:        row.CpuPct,
			MemBytes:      row.MemBytes,
			DiskBytes:     row.DiskBytes,
		}
	}

	writeJSON(w, http.StatusOK, metricsResponse{
		SandboxID: sandboxID,
		Range:     rangeTier,
		Points:    points,
	})
}
