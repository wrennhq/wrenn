package api

import (
	"log/slog"
	"net/http"
	"time"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/service"
)

type statsHandler struct {
	svc *service.StatsService
}

func newStatsHandler(svc *service.StatsService) *statsHandler {
	return &statsHandler{svc: svc}
}

type statsCurrentResponse struct {
	RunningCount     int32 `json:"running_count"`
	VCPUsReserved    int32 `json:"vcpus_reserved"`
	MemoryMBReserved int32 `json:"memory_mb_reserved"`
}

type statsPeaksResponse struct {
	RunningCount int32 `json:"running_count"`
	VCPUs        int32 `json:"vcpus"`
	MemoryMB     int32 `json:"memory_mb"`
}

type statsSeriesResponse struct {
	Labels   []string `json:"labels"`
	Running  []int32  `json:"running"`
	VCPUs    []int32  `json:"vcpus"`
	MemoryMB []int32  `json:"memory_mb"`
}

type statsResponse struct {
	Range   string               `json:"range"`
	Current statsCurrentResponse `json:"current"`
	Peaks   statsPeaksResponse   `json:"peaks"`
	Series  statsSeriesResponse  `json:"series"`
}

// GetStats handles GET /v1/sandboxes/stats?range=5m|1h|6h|24h|30d
func (h *statsHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = string(service.Range1h)
	}
	tr := service.TimeRange(rangeParam)
	if !service.ValidRange(tr) {
		writeError(w, http.StatusBadRequest, "invalid_request", "range must be one of: 5m, 1h, 6h, 24h, 30d")
		return
	}

	current, peaks, series, err := h.svc.GetStats(r.Context(), ac.TeamID, tr)
	if err != nil {
		slog.Error("stats handler: get stats failed", "team_id", ac.TeamID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to retrieve stats")
		return
	}

	resp := statsResponse{
		Range: rangeParam,
		Current: statsCurrentResponse{
			RunningCount:     current.RunningCount,
			VCPUsReserved:    current.VCPUsReserved,
			MemoryMBReserved: current.MemoryMBReserved,
		},
		Peaks: statsPeaksResponse{
			RunningCount: peaks.RunningCount,
			VCPUs:        peaks.VCPUs,
			MemoryMB:     peaks.MemoryMB,
		},
		Series: statsSeriesResponse{
			Labels:   make([]string, len(series)),
			Running:  make([]int32, len(series)),
			VCPUs:    make([]int32, len(series)),
			MemoryMB: make([]int32, len(series)),
		},
	}

	for i, pt := range series {
		resp.Series.Labels[i] = pt.Bucket.UTC().Format(time.RFC3339)
		resp.Series.Running[i] = pt.RunningCount
		resp.Series.VCPUs[i] = pt.VCPUsReserved
		resp.Series.MemoryMB[i] = pt.MemoryMBReserved
	}

	writeJSON(w, http.StatusOK, resp)
}
