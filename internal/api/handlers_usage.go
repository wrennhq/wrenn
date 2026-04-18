package api

import (
	"log/slog"
	"net/http"
	"time"

	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
)

type usageHandler struct {
	svc *service.UsageService
}

func newUsageHandler(svc *service.UsageService) *usageHandler {
	return &usageHandler{svc: svc}
}

type usagePointResponse struct {
	Date         string  `json:"date"`
	CPUMinutes   float64 `json:"cpu_minutes"`
	RAMMBMinutes float64 `json:"ram_mb_minutes"`
}

type usageResponse struct {
	From   string               `json:"from"`
	To     string               `json:"to"`
	Points []usagePointResponse `json:"points"`
}

// GetUsage handles GET /v1/capsules/usage?from=YYYY-MM-DD&to=YYYY-MM-DD
func (h *usageHandler) GetUsage(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var from, to time.Time
	if s := r.URL.Query().Get("from"); s != "" {
		var err error
		from, err = time.Parse("2006-01-02", s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "from must be YYYY-MM-DD")
			return
		}
	} else {
		from = today.AddDate(0, 0, -29)
	}

	if s := r.URL.Query().Get("to"); s != "" {
		var err error
		to, err = time.Parse("2006-01-02", s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "to must be YYYY-MM-DD")
			return
		}
	} else {
		to = today
	}

	if from.After(to) {
		writeError(w, http.StatusBadRequest, "invalid_request", "from must be before or equal to to")
		return
	}
	if to.Sub(from).Hours()/24 > 92 {
		writeError(w, http.StatusBadRequest, "invalid_request", "range cannot exceed 92 days")
		return
	}

	points, err := h.svc.GetUsage(r.Context(), ac.TeamID, from, to)
	if err != nil {
		slog.Error("usage handler: get usage failed", "team_id", ac.TeamID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to retrieve usage")
		return
	}

	resp := usageResponse{
		From:   from.Format("2006-01-02"),
		To:     to.Format("2006-01-02"),
		Points: make([]usagePointResponse, len(points)),
	}
	for i, pt := range points {
		resp.Points[i] = usagePointResponse{
			Date:         pt.Day.Format("2006-01-02"),
			CPUMinutes:   pt.CPUMinutes,
			RAMMBMinutes: pt.RAMMBMinutes,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
