package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/layout"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	"git.omukk.dev/wrenn/sandbox/internal/service"
	"git.omukk.dev/wrenn/sandbox/internal/validate"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
)

type buildHandler struct {
	svc  *service.BuildService
	db   *db.Queries
	pool *lifecycle.HostClientPool
}

func newBuildHandler(svc *service.BuildService, db *db.Queries, pool *lifecycle.HostClientPool) *buildHandler {
	return &buildHandler{svc: svc, db: db, pool: pool}
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
		ID:           id.FormatBuildID(b.ID),
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
	if b.Healthcheck != "" {
		resp.Healthcheck = &b.Healthcheck
	}
	if b.Error != "" {
		resp.Error = &b.Error
	}
	if b.SandboxID.Valid {
		s := id.FormatSandboxID(b.SandboxID)
		resp.SandboxID = &s
	}
	if b.HostID.Valid {
		s := id.FormatHostID(b.HostID)
		resp.HostID = &s
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
		slog.Error("failed to create build", "error", err)
		writeError(w, http.StatusInternalServerError, "build_error", "failed to create build")
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
	buildIDStr := chi.URLParam(r, "id")

	buildID, err := id.ParseBuildID(buildIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid build ID")
		return
	}

	build, err := h.svc.Get(r.Context(), buildID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "build not found")
		return
	}

	writeJSON(w, http.StatusOK, buildToResponse(build))
}

// ListTemplates handles GET /v1/admin/templates — returns all templates across all teams.
func (h *buildHandler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	templates, err := h.db.ListTemplates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list templates")
		return
	}

	type templateResponse struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		VCPUs     int32  `json:"vcpus"`
		MemoryMB  int32  `json:"memory_mb"`
		SizeBytes int64  `json:"size_bytes"`
		TeamID    string `json:"team_id"`
		CreatedAt string `json:"created_at"`
	}

	resp := make([]templateResponse, len(templates))
	for i, t := range templates {
		resp[i] = templateResponse{
			Name:      t.Name,
			Type:      t.Type,
			VCPUs:     t.Vcpus,
			MemoryMB:  t.MemoryMb,
			SizeBytes: t.SizeBytes,
			TeamID:    id.FormatTeamID(t.TeamID),
		}
		if t.CreatedAt.Valid {
			resp[i].CreatedAt = t.CreatedAt.Time.Format(time.RFC3339)
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// DeleteTemplate handles DELETE /v1/admin/templates/{name}.
func (h *buildHandler) DeleteTemplate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.SafeName(name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("invalid template name: %s", err))
		return
	}
	ctx := r.Context()

	tmpl, err := h.db.GetPlatformTemplateByName(ctx, name)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "template not found")
		return
	}
	if layout.IsMinimal(tmpl.TeamID, tmpl.ID) {
		writeError(w, http.StatusForbidden, "forbidden", "the minimal template cannot be deleted")
		return
	}

	// Broadcast delete to all online hosts.
	hosts, _ := h.db.ListActiveHosts(ctx)
	for _, host := range hosts {
		if host.Status != "online" {
			continue
		}
		agent, err := h.pool.GetForHost(host)
		if err != nil {
			continue
		}
		if _, err := agent.DeleteSnapshot(ctx, connect.NewRequest(&pb.DeleteSnapshotRequest{
			TeamId:     formatUUIDForRPC(tmpl.TeamID),
			TemplateId: formatUUIDForRPC(tmpl.ID),
		})); err != nil {
			if connect.CodeOf(err) != connect.CodeNotFound {
				slog.Warn("admin: failed to delete template on host", "host_id", id.FormatHostID(host.ID), "name", name, "error", err)
			}
		}
	}

	if err := h.db.DeleteTemplate(ctx, tmpl.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to delete template record")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
