package api

import (
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
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

// GetMetrics handles GET /v1/capsules/{id}/metrics?range=10m|2h|24h.
func (h *sandboxMetricsHandler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid sandbox ID."))
		return
	}

	rangeTier := r.URL.Query().Get("range")
	if rangeTier == "" {
		rangeTier = "10m"
	}
	validRanges := map[string]bool{"5m": true, "10m": true, "1h": true, "2h": true, "6h": true, "12h": true, "24h": true}
	if !validRanges[rangeTier] {
		writeErr(w, r, apperr.ValidationFailed.Msg("The range parameter must be one of: 5m, 10m, 1h, 2h, 6h, 12h, 24h.").With("field", "range"))
		return
	}

	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeErr(w, r, apperr.SandboxNotFound.Wrap(err))
		return
	}

	switch sb.Status {
	case "running":
		h.getFromAgent(w, r, sandboxIDStr, rangeTier, sb.HostID)
	case "paused":
		h.getFromDB(w, r, sandboxIDStr, sandboxID, rangeTier)
	default:
		writeErr(w, r, apperr.NotFound.Msg("Metrics are not available for a sandbox in state "+sb.Status+".").With("status", sb.Status))
	}
}

func (h *sandboxMetricsHandler) getFromAgent(w http.ResponseWriter, r *http.Request, sandboxIDStr, rangeTier string, hostID pgtype.UUID) {
	ctx := r.Context()

	agent, err := agentForHost(ctx, h.db, h.pool, hostID)
	if err != nil {
		writeErr(w, r, apperr.HostUnreachable.Wrap(err))
		return
	}

	resp, err := agent.GetSandboxMetrics(ctx, connect.NewRequest(&pb.GetSandboxMetricsRequest{
		SandboxId: sandboxIDStr,
		Range:     rangeTier,
	}))
	if err != nil {
		writeErr(w, r, err)
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
		SandboxID: sandboxIDStr,
		Range:     rangeTier,
		Points:    points,
	})
}

// rangeToDB maps a user-facing range filter to the DB tier and cutoff duration.
var rangeToDB = map[string]struct {
	tier   string
	cutoff time.Duration
}{
	"5m":  {"10m", 5 * time.Minute},
	"10m": {"10m", 10 * time.Minute},
	"1h":  {"2h", 1 * time.Hour},
	"2h":  {"2h", 2 * time.Hour},
	"6h":  {"24h", 6 * time.Hour},
	"12h": {"24h", 12 * time.Hour},
	"24h": {"24h", 24 * time.Hour},
}

func (h *sandboxMetricsHandler) getFromDB(w http.ResponseWriter, r *http.Request, sandboxIDStr string, sandboxID pgtype.UUID, rangeTier string) {
	ctx := r.Context()
	mapping := rangeToDB[rangeTier]
	rows, err := h.db.GetSandboxMetricPoints(ctx, db.GetSandboxMetricPointsParams{
		SandboxID: sandboxID,
		Tier:      mapping.tier,
		Ts:        time.Now().Add(-mapping.cutoff).Unix(),
	})
	if err != nil {
		writeErr(w, r, apperr.Internal.Wrap(err))
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
		SandboxID: sandboxIDStr,
		Range:     rangeTier,
		Points:    points,
	})
}
