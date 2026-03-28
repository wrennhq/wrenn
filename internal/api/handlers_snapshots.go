package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/audit"
	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	"git.omukk.dev/wrenn/sandbox/internal/id"
	"git.omukk.dev/wrenn/sandbox/internal/lifecycle"
	"git.omukk.dev/wrenn/sandbox/internal/service"
	"git.omukk.dev/wrenn/sandbox/internal/validate"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
)

type snapshotHandler struct {
	svc   *service.TemplateService
	db    *db.Queries
	pool  *lifecycle.HostClientPool
	audit *audit.AuditLogger
}

func newSnapshotHandler(svc *service.TemplateService, db *db.Queries, pool *lifecycle.HostClientPool, al *audit.AuditLogger) *snapshotHandler {
	return &snapshotHandler{svc: svc, db: db, pool: pool, audit: al}
}

// deleteSnapshotBroadcast attempts to delete snapshot files on all online hosts.
// Snapshots aren't currently host-tracked in the DB, so we broadcast to all hosts
// and ignore NotFound errors.
func (h *snapshotHandler) deleteSnapshotBroadcast(ctx context.Context, teamID, templateID pgtype.UUID) error {
	hosts, err := h.db.ListActiveHosts(ctx)
	if err != nil {
		return fmt.Errorf("list hosts: %w", err)
	}
	for _, host := range hosts {
		if host.Status != "online" {
			continue
		}
		agent, err := h.pool.GetForHost(host)
		if err != nil {
			continue
		}
		if _, err := agent.DeleteSnapshot(ctx, connect.NewRequest(&pb.DeleteSnapshotRequest{
			TeamId:     formatUUIDForRPC(teamID),
			TemplateId: formatUUIDForRPC(templateID),
		})); err != nil {
			if connect.CodeOf(err) != connect.CodeNotFound {
				slog.Warn("snapshot: failed to delete on host", "host_id", id.FormatHostID(host.ID), "error", err)
			}
		}
	}
	return nil
}

type createSnapshotRequest struct {
	SandboxID string `json:"sandbox_id"`
	Name      string `json:"name"`
}

type snapshotResponse struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	VCPUs     *int32 `json:"vcpus,omitempty"`
	MemoryMB  *int32 `json:"memory_mb,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
	Platform  bool   `json:"platform"`
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
	return resp
}

// Create handles POST /v1/snapshots.
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
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox_id")
		return
	}

	if req.Name == "" {
		req.Name = id.NewSnapshotName()
	}
	if err := validate.SafeName(req.Name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("invalid snapshot name: %s", err))
		return
	}

	ctx := r.Context()
	ac := auth.MustFromContext(ctx)
	overwrite := r.URL.Query().Get("overwrite") == "true"

	// Check for global name collision.
	if _, err := h.db.GetPlatformTemplateByName(ctx, req.Name); err == nil {
		writeError(w, http.StatusConflict, "name_reserved", "template name is reserved by a global template")
		return
	}

	// Check if name already exists for this team.
	if existing, err := h.db.GetTemplateByTeam(ctx, db.GetTemplateByTeamParams{Name: req.Name, TeamID: ac.TeamID}); err == nil {
		if !overwrite {
			writeError(w, http.StatusConflict, "already_exists", "snapshot name already exists; use ?overwrite=true to replace")
			return
		}
		// Delete old snapshot files from all hosts before removing the DB record.
		if err := h.deleteSnapshotBroadcast(ctx, existing.TeamID, existing.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "agent_error", "failed to delete existing snapshot files")
			return
		}
		if err := h.db.DeleteTemplateByTeam(ctx, db.DeleteTemplateByTeamParams{Name: req.Name, TeamID: ac.TeamID}); err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "failed to remove existing template record")
			return
		}
	}

	// Verify sandbox exists, belongs to team, and is running or paused.
	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sb.Status != "running" && sb.Status != "paused" {
		writeError(w, http.StatusConflict, "invalid_state", "sandbox must be running or paused")
		return
	}

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host_unavailable", "sandbox host is not reachable")
		return
	}

	// Pre-mark sandbox as "paused" in DB BEFORE issuing the snapshot RPC.
	// The host agent's CreateSnapshot removes the sandbox from its in-memory
	// map immediately; if the reconciler fires during the flatten window and
	// the DB still says "running", it will mark the sandbox "stopped".
	if sb.Status == "running" {
		if _, err := h.db.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
			ID: sandboxID, Status: "paused",
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "failed to update sandbox status")
			return
		}
	}

	// Use a detached context with a generous timeout so the snapshot completes
	// even if the client disconnects (the flatten step can take 10-20s).
	snapCtx, snapCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer snapCancel()

	// Generate the new template ID upfront so the host agent knows where to store files.
	newTemplateID := id.NewTemplateID()

	resp, err := agent.CreateSnapshot(snapCtx, connect.NewRequest(&pb.CreateSnapshotRequest{
		SandboxId:  req.SandboxID,
		Name:       req.Name,
		TeamId:     formatUUIDForRPC(ac.TeamID),
		TemplateId: formatUUIDForRPC(newTemplateID),
	}))
	if err != nil {
		// Snapshot failed — revert status back to what it was.
		if sb.Status == "running" {
			if _, dbErr := h.db.UpdateSandboxStatus(snapCtx, db.UpdateSandboxStatusParams{
				ID: sandboxID, Status: "running",
			}); dbErr != nil {
				slog.Error("failed to revert sandbox status after snapshot error", "sandbox_id", req.SandboxID, "error", dbErr)
			}
		}
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	tmpl, err := h.db.InsertTemplate(snapCtx, db.InsertTemplateParams{
		ID:        newTemplateID,
		Name:      req.Name,
		Type:      "snapshot",
		Vcpus:     sb.Vcpus,
		MemoryMb:  sb.MemoryMb,
		SizeBytes: resp.Msg.SizeBytes,
		TeamID:    ac.TeamID,
	})
	if err != nil {
		slog.Error("failed to insert template record", "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "db_error", "snapshot created but failed to record in database")
		return
	}

	h.audit.LogSnapshotCreate(snapCtx, ac, req.Name)

	if ctx.Err() != nil {
		slog.Info("snapshot created but client disconnected before response", "name", req.Name)
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

	if err := h.deleteSnapshotBroadcast(ctx, tmpl.TeamID, tmpl.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "agent_error", "failed to delete snapshot files")
		return
	}

	if err := h.db.DeleteTemplateByTeam(ctx, db.DeleteTemplateByTeamParams{Name: name, TeamID: ac.TeamID}); err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to delete template record")
		return
	}

	h.audit.LogSnapshotDelete(r.Context(), ac, name)
	w.WriteHeader(http.StatusNoContent)
}
