package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/layout"
	"git.omukk.dev/wrenn/wrenn/pkg/audit"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	"git.omukk.dev/wrenn/wrenn/pkg/service"
	"git.omukk.dev/wrenn/wrenn/pkg/validate"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

type snapshotHandler struct {
	svc        *service.TemplateService
	sandboxSvc *service.SandboxService
	db         *db.Queries
	pool       *lifecycle.HostClientPool
	audit      *audit.AuditLogger
}

func newSnapshotHandler(svc *service.TemplateService, sandboxSvc *service.SandboxService, db *db.Queries, pool *lifecycle.HostClientPool, al *audit.AuditLogger) *snapshotHandler {
	return &snapshotHandler{svc: svc, sandboxSvc: sandboxSvc, db: db, pool: pool, audit: al}
}

// deleteSnapshotEverywhere removes a template's files from every active host.
// Templates aren't host-tracked in the DB, so it broadcasts to all hosts.
//
// It is strict by design: deletion is reported successful only when every
// active host has either removed the files or reported NotFound (it never
// held them). If any host is offline or returns an error, it returns an error
// and the caller MUST NOT delete the DB record — doing so would orphan the
// files on disk with no record left to retry against.
func deleteSnapshotEverywhere(ctx context.Context, queries *db.Queries, pool *lifecycle.HostClientPool, teamID, templateID pgtype.UUID) error {
	hosts, err := queries.ListActiveHosts(ctx)
	if err != nil {
		return fmt.Errorf("list hosts: %w", err)
	}
	for _, host := range hosts {
		if host.Status != "online" {
			return fmt.Errorf("host %s is %s — cannot guarantee snapshot file removal",
				id.FormatHostID(host.ID), host.Status)
		}
		agent, err := pool.GetForHost(host)
		if err != nil {
			return fmt.Errorf("connect to host %s: %w", id.FormatHostID(host.ID), err)
		}
		if _, err := agent.DeleteSnapshot(ctx, connect.NewRequest(&pb.DeleteSnapshotRequest{
			TeamId:     formatUUIDForRPC(teamID),
			TemplateId: formatUUIDForRPC(templateID),
		})); err != nil {
			// NotFound just means this host never held the template.
			if connect.CodeOf(err) == connect.CodeNotFound {
				continue
			}
			return fmt.Errorf("delete snapshot on host %s: %w", id.FormatHostID(host.ID), err)
		}
	}
	return nil
}

type snapshotResponse struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	VCPUs     *int32            `json:"vcpus,omitempty"`
	MemoryMB  *int32            `json:"memory_mb,omitempty"`
	SizeBytes int64             `json:"size_bytes"`
	CreatedAt string            `json:"created_at"`
	Platform  bool              `json:"platform"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

func templateToResponse(t db.Template) snapshotResponse {
	resp := snapshotResponse{
		Name:      t.Name,
		Type:      t.Type,
		SizeBytes: t.SizeBytes,
		Platform:  t.TeamID == id.PlatformTeamID,
	}
	if t.Vcpus != 0 {
		resp.VCPUs = &t.Vcpus
	}
	if t.MemoryMb != 0 {
		resp.MemoryMB = &t.MemoryMb
	}
	if t.CreatedAt.Valid {
		resp.CreatedAt = t.CreatedAt.Time.Format(time.RFC3339)
	}
	if len(t.Metadata) > 0 {
		var meta map[string]string
		if err := json.Unmarshal(t.Metadata, &meta); err == nil && len(meta) > 0 {
			resp.Metadata = meta
		}
	}
	return resp
}

type createSnapshotRequest struct {
	SandboxID string `json:"sandbox_id"`
	Name      string `json:"name"`
}

// Create handles POST /v1/snapshots. Takes a live snapshot of a running
// sandbox and registers the result as a new template.
func (h *snapshotHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.SandboxID == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "sandbox_id is required")
		return
	}
	sandboxID, err := id.ParseSandboxID(req.SandboxID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}
	ac := auth.MustFromContext(r.Context())

	tmpl, err := h.sandboxSvc.CreateSnapshot(r.Context(), sandboxID, ac.TeamID, req.Name)
	name := req.Name
	if err == nil {
		name = tmpl.Name
	}
	h.audit.LogSnapshotCreate(r.Context(), ac, name, err)
	if err != nil {
		if name != "" {
			h.audit.LogSnapshotDeleteSystem(r.Context(), ac.TeamID, name, "cleanup_after_create_error", nil)
		}
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusCreated, templateToResponse(tmpl))
}

// List handles GET /v1/snapshots.
func (h *snapshotHandler) List(w http.ResponseWriter, r *http.Request) {
	ac := auth.MustFromContext(r.Context())
	typeFilter := r.URL.Query().Get("type")

	templates, err := h.svc.List(r.Context(), ac.TeamID, typeFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list templates")
		return
	}

	resp := make([]snapshotResponse, len(templates))
	for i, t := range templates {
		resp[i] = templateToResponse(t)
	}

	writeJSON(w, http.StatusOK, resp)
}

// Delete handles DELETE /v1/snapshots/{name}.
func (h *snapshotHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := validate.SafeName(name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("invalid snapshot name: %s", err))
		return
	}
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	tmpl, err := h.db.GetTemplateByTeam(ctx, db.GetTemplateByTeamParams{Name: name, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "template not found")
		return
	}
	// Platform templates can only be deleted by admins via /v1/admin/templates.
	if tmpl.TeamID == id.PlatformTeamID {
		writeError(w, http.StatusForbidden, "forbidden", "platform templates cannot be deleted here")
		return
	}
	if layout.IsMinimal(tmpl.TeamID, tmpl.ID) {
		writeError(w, http.StatusForbidden, "forbidden", "the minimal template cannot be deleted")
		return
	}

	if err := deleteSnapshotEverywhere(ctx, h.db, h.pool, tmpl.TeamID, tmpl.ID); err != nil {
		h.audit.LogSnapshotDelete(r.Context(), ac, name, err)
		writeError(w, http.StatusConflict, "delete_failed",
			"could not remove snapshot files from all hosts: "+err.Error())
		return
	}

	if err := h.db.DeleteTemplateByTeam(ctx, db.DeleteTemplateByTeamParams{Name: name, TeamID: ac.TeamID}); err != nil {
		h.audit.LogSnapshotDelete(r.Context(), ac, name, err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to delete template record")
		return
	}

	h.audit.LogSnapshotDelete(r.Context(), ac, name, nil)
	w.WriteHeader(http.StatusNoContent)
}
