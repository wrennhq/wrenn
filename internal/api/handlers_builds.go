package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/layout"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
	"git.omukk.dev/wrenn/wrenn/pkg/validate"
)

type buildHandler struct {
	svc         *service.BuildService
	templateSvc *service.TemplateService
	db          *db.Queries
	pool        *lifecycle.HostClientPool
	audit       *audit.AuditLogger
}

func newBuildHandler(svc *service.BuildService, templateSvc *service.TemplateService, db *db.Queries, pool *lifecycle.HostClientPool, al *audit.AuditLogger) *buildHandler {
	return &buildHandler{svc: svc, templateSvc: templateSvc, db: db, pool: pool, audit: al}
}

type createBuildRequest struct {
	Name         string   `json:"name"`
	BaseTemplate string   `json:"base_template"`
	Recipe       []string `json:"recipe"`
	Healthcheck  string   `json:"healthcheck"`
	VCPUs        int32    `json:"vcpus"`
	MemoryMB     int32    `json:"memory_mb"`
	SkipPrePost  bool     `json:"skip_pre_post"`
	RunAsRoot    bool     `json:"run_as_root"`
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
	DefaultUser  string          `json:"default_user"`
	DefaultEnv   json.RawMessage `json:"default_env"`
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
		DefaultUser:  b.DefaultUser,
		DefaultEnv:   b.DefaultEnv,
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
// Accepts either JSON body or multipart/form-data with a "config" JSON part
// and an optional "archive" file part (tar/tar.gz/zip for COPY commands).
func (h *buildHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createBuildRequest
	var archive []byte
	var archiveName string

	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/") {
		// 100 MB max for multipart (archive + JSON config).
		if err := r.ParseMultipartForm(100 << 20); err != nil {
			writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Failed to parse the multipart form."))
			return
		}

		// Parse JSON config from "config" field.
		configStr := r.FormValue("config")
		if configStr == "" {
			writeErr(w, r, apperr.ValidationFailed.Msg("The config field is required in the multipart form.").With("field", "config"))
			return
		}
		if err := json.Unmarshal([]byte(configStr), &req); err != nil {
			writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid config JSON in the multipart form."))
			return
		}

		// Read optional archive file (max 100 MB).
		file, header, err := r.FormFile("archive")
		if err == nil {
			defer file.Close()
			const maxArchiveSize = 100 << 20 // 100 MB
			lr := io.LimitReader(file, maxArchiveSize+1)
			archive, err = io.ReadAll(lr)
			if err != nil {
				writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Failed to read the archive file."))
				return
			}
			if int64(len(archive)) > maxArchiveSize {
				writeErr(w, r, apperr.PayloadTooLarge.Msg("The archive exceeds the 100 MB limit."))
				return
			}
			archiveName = header.Filename
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
			return
		}
	}

	if req.Name == "" {
		writeErr(w, r, apperr.ValidationFailed.Msg("The name field is required.").With("field", "name"))
		return
	}
	if err := validate.SafeName(req.Name); err != nil {
		writeErr(w, r, apperr.ValidationFailed.WrapMsg(err, "Invalid template name.").With("field", "name"))
		return
	}
	if len(req.Recipe) == 0 {
		writeErr(w, r, apperr.ValidationFailed.Msg("The recipe must contain at least one command.").With("field", "recipe"))
		return
	}

	build, err := h.svc.Create(r.Context(), service.BuildCreateParams{
		Name:         req.Name,
		BaseTemplate: req.BaseTemplate,
		Recipe:       req.Recipe,
		Healthcheck:  req.Healthcheck,
		VCPUs:        req.VCPUs,
		MemoryMB:     req.MemoryMB,
		SkipPrePost:  req.SkipPrePost,
		RunAsRoot:    req.RunAsRoot,
		Archive:      archive,
		ArchiveName:  archiveName,
	})
	if err != nil {
		slog.Error("failed to create build", "error", err)
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	ac := auth.MustFromContext(r.Context())
	h.audit.LogBuildCreate(r.Context(), ac, build.ID, req.Name)
	writeJSON(w, http.StatusCreated, buildToResponse(build))
}

// List handles GET /v1/admin/builds.
func (h *buildHandler) List(w http.ResponseWriter, r *http.Request) {
	builds, err := h.svc.List(r.Context())
	if err != nil {
		writeErr(w, r, apperr.Internal.Wrap(err))
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
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid build ID."))
		return
	}

	build, err := h.svc.Get(r.Context(), buildID)
	if err != nil {
		writeErr(w, r, apperr.BuildNotFound.Wrap(err))
		return
	}

	writeJSON(w, http.StatusOK, buildToResponse(build))
}

// ListTemplates handles GET /v1/admin/templates — returns all templates across all teams.
func (h *buildHandler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	templates, err := h.db.ListTemplates(r.Context())
	if err != nil {
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	// Resolve actual on-disk sizes for templates with unknown size.
	templates = resolveTemplateSizes(r.Context(), h.db, h.pool, templates)

	type templateResponse struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		VCPUs     int32  `json:"vcpus"`
		MemoryMB  int32  `json:"memory_mb"`
		SizeBytes int64  `json:"size_bytes"`
		TeamID    string `json:"team_id"`
		CreatedAt string `json:"created_at"`
		Protected bool   `json:"protected"`
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
			Protected: layout.IsSystemTemplate(t.TeamID, t.ID),
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
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid template name."))
		return
	}
	ctx := r.Context()

	tmpl, err := h.db.GetPlatformTemplateByName(ctx, name)
	if err != nil {
		writeErr(w, r, apperr.TemplateNotFound.Wrap(err))
		return
	}
	if layout.IsSystemTemplate(tmpl.TeamID, tmpl.ID) {
		writeErr(w, r, apperr.TemplateProtected.New())
		return
	}

	// Remove the files from every host before dropping the DB record, so a
	// failure leaves the template intact and retryable rather than orphaned.
	if err := deleteSnapshotEverywhere(ctx, h.db, h.pool, tmpl.TeamID, tmpl.ID); err != nil {
		writeErr(w, r, apperr.Conflict.WrapMsg(err, "Could not remove template files from all hosts. Try again when all hosts are online."))
		return
	}

	if err := h.db.DeleteTemplate(ctx, tmpl.ID); err != nil {
		writeErr(w, r, apperr.Internal.Wrap(err))
		return
	}

	ac := auth.MustFromContext(r.Context())
	h.audit.LogTemplateDelete(r.Context(), ac, name)
	w.WriteHeader(http.StatusNoContent)
}

// RenameTemplate handles PATCH /v1/admin/templates/{name}. Renames a platform
// template. The hardcoded system base templates (minimal-ubuntu / -alpine /
// -arch / -fedora) cannot be renamed.
func (h *buildHandler) RenameTemplate(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.SafeName(name); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid template name."))
		return
	}
	ctx := r.Context()

	var req renameTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}

	tmpl, err := h.db.GetPlatformTemplateByName(ctx, name)
	if err != nil {
		writeErr(w, r, apperr.TemplateNotFound.Wrap(err))
		return
	}
	if layout.IsSystemTemplate(tmpl.TeamID, tmpl.ID) {
		writeErr(w, r, apperr.TemplateProtected.New())
		return
	}

	if _, err := h.templateSvc.Rename(ctx, tmpl.ID, req.NewName); err != nil {
		writeErr(w, r, err)
		return
	}

	ac := auth.MustFromContext(ctx)
	h.audit.LogTemplateRenameAdmin(ctx, ac, name, strings.TrimSpace(req.NewName))
	w.WriteHeader(http.StatusNoContent)
}

// Cancel handles POST /v1/admin/builds/{id}/cancel.
func (h *buildHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	buildIDStr := chi.URLParam(r, "id")

	buildID, err := id.ParseBuildID(buildIDStr)
	if err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid build ID."))
		return
	}

	if err := h.svc.Cancel(r.Context(), buildID); err != nil {
		// Cancel returns typed apperr errors (BuildNotFound / Conflict); pass
		// them through so not-found stays 404 rather than collapsing to 409.
		writeErr(w, r, err)
		return
	}

	ac := auth.MustFromContext(r.Context())
	h.audit.LogBuildCancel(r.Context(), ac, buildID)
	w.WriteHeader(http.StatusNoContent)
}
