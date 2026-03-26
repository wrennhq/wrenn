package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/service"
	"git.omukk.dev/wrenn/sandbox/internal/validate"
)

type buildHandler struct {
	svc *service.BuildService
}

func newBuildHandler(svc *service.BuildService) *buildHandler {
	return &buildHandler{svc: svc}
}

type createBuildRequest struct {
	Name         string   `json:"name"`
	BaseTemplate string   `json:"base_template"`
	Recipe       []string `json:"recipe"`
	Healthcheck  string   `json:"healthcheck"`
	VCPUs        int32    `json:"vcpus"`
	MemoryMB     int32    `json:"memory_mb"`
}

type buildResponse struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	BaseTemplate string          `json:"base_template"`
	Recipe       json.RawMessage `json:"recipe"`
	Healthcheck  *string         `json:"healthcheck,omitempty"`
	VCPUs        int32           `json:"vcpus"`
	MemoryMB     int32           `json:"memory_mb"`
	Status       string          `json:"status"`
	CurrentStep  int32           `json:"current_step"`
	TotalSteps   int32           `json:"total_steps"`
	Logs         json.RawMessage `json:"logs"`
	Error        *string         `json:"error,omitempty"`
	SandboxID    *string         `json:"sandbox_id,omitempty"`
	HostID       *string         `json:"host_id,omitempty"`
	CreatedAt    string          `json:"created_at"`
	StartedAt    *string         `json:"started_at,omitempty"`
	CompletedAt  *string         `json:"completed_at,omitempty"`
}

func buildToResponse(b db.TemplateBuild) buildResponse {
	resp := buildResponse{
		ID:           b.ID,
		Name:         b.Name,
		BaseTemplate: b.BaseTemplate,
		Recipe:       b.Recipe,
		VCPUs:        b.Vcpus,
		MemoryMB:     b.MemoryMb,
		Status:       b.Status,
		CurrentStep:  b.CurrentStep,
		TotalSteps:   b.TotalSteps,
		Logs:         b.Logs,
	}
	if b.Healthcheck.Valid {
		resp.Healthcheck = &b.Healthcheck.String
	}
	if b.Error.Valid {
		resp.Error = &b.Error.String
	}
	if b.SandboxID.Valid {
		resp.SandboxID = &b.SandboxID.String
	}
	if b.HostID.Valid {
		resp.HostID = &b.HostID.String
	}
	if b.CreatedAt.Valid {
		resp.CreatedAt = b.CreatedAt.Time.Format(time.RFC3339)
	}
	if b.StartedAt.Valid {
		s := b.StartedAt.Time.Format(time.RFC3339)
		resp.StartedAt = &s
	}
	if b.CompletedAt.Valid {
		s := b.CompletedAt.Time.Format(time.RFC3339)
		resp.CompletedAt = &s
	}
	return resp
}

// Create handles POST /v1/admin/builds.
func (h *buildHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createBuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	if err := validate.SafeName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("invalid template name: %s", err))
		return
	}
	if len(req.Recipe) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "recipe must contain at least one command")
		return
	}

	build, err := h.svc.Create(r.Context(), service.BuildCreateParams{
		Name:         req.Name,
		BaseTemplate: req.BaseTemplate,
		Recipe:       req.Recipe,
		Healthcheck:  req.Healthcheck,
		VCPUs:        req.VCPUs,
		MemoryMB:     req.MemoryMB,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "build_error", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, buildToResponse(build))
}

// List handles GET /v1/admin/builds.
func (h *buildHandler) List(w http.ResponseWriter, r *http.Request) {
	builds, err := h.svc.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list builds")
		return
	}

	resp := make([]buildResponse, len(builds))
	for i, b := range builds {
		resp[i] = buildToResponse(b)
	}

	writeJSON(w, http.StatusOK, resp)
}

// Get handles GET /v1/admin/builds/{id}.
func (h *buildHandler) Get(w http.ResponseWriter, r *http.Request) {
	buildID := chi.URLParam(r, "id")

	build, err := h.svc.Get(r.Context(), buildID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "build not found")
		return
	}

	writeJSON(w, http.StatusOK, buildToResponse(build))
}
