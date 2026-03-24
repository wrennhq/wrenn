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
// and ignore NotFound errors. TODO: add host_id to templates table.
func (h *snapshotHandler) deleteSnapshotBroadcast(ctx context.Context, name string) error {
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
		if _, err := agent.DeleteSnapshot(ctx, connect.NewRequest(&pb.DeleteSnapshotRequest{Name: name})); err != nil {
			if connect.CodeOf(err) != connect.CodeNotFound {
				slog.Warn("snapshot: failed to delete on host", "host_id", host.ID, "name", name, "error", err)
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
}

func templateToResponse(t db.Template) snapshotResponse {
	resp := snapshotResponse{
		Name:      t.Name,
		Type:      t.Type,
		SizeBytes: t.SizeBytes,
	}
	if t.Vcpus.Valid {
		resp.VCPUs = &t.Vcpus.Int32
	}
	if t.MemoryMb.Valid {
		resp.MemoryMB = &t.MemoryMb.Int32
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

	// Check if name already exists for this team.
	if _, err := h.db.GetTemplateByTeam(ctx, db.GetTemplateByTeamParams{Name: req.Name, TeamID: ac.TeamID}); err == nil {
		if !overwrite {
			writeError(w, http.StatusConflict, "already_exists", "snapshot name already exists; use ?overwrite=true to replace")
			return
		}
		// Delete old snapshot files from all hosts before removing the DB record.
		if err := h.deleteSnapshotBroadcast(ctx, req.Name); err != nil {
			writeError(w, http.StatusInternalServerError, "agent_error", "failed to delete existing snapshot files")
			return
		}
		if err := h.db.DeleteTemplateByTeam(ctx, db.DeleteTemplateByTeamParams{Name: req.Name, TeamID: ac.TeamID}); err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "failed to remove existing template record")
			return
		}
	}

	// Verify sandbox exists, belongs to team, and is running or paused.
	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: req.SandboxID, TeamID: ac.TeamID})
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

	resp, err := agent.CreateSnapshot(ctx, connect.NewRequest(&pb.CreateSnapshotRequest{
		SandboxId: req.SandboxID,
		Name:      req.Name,
	}))
	if err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	// Mark sandbox as paused (if it was running, it got paused by the snapshot).
	if sb.Status != "paused" {
		if _, err := h.db.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
			ID: req.SandboxID, Status: "paused",
		}); err != nil {
			slog.Error("failed to update sandbox status after snapshot", "sandbox_id", req.SandboxID, "error", err)
		}
	}

	tmpl, err := h.db.InsertTemplate(ctx, db.InsertTemplateParams{
		Name:      req.Name,
		Type:      "snapshot",
		Vcpus:     pgtype.Int4{Int32: sb.Vcpus, Valid: true},
		MemoryMb:  pgtype.Int4{Int32: sb.MemoryMb, Valid: true},
		SizeBytes: resp.Msg.SizeBytes,
		TeamID:    ac.TeamID,
	})
	if err != nil {
		slog.Error("failed to insert template record", "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "db_error", "snapshot created but failed to record in database")
		return
	}

	h.audit.LogSnapshotCreate(r.Context(), ac, req.Name)
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

	if _, err := h.db.GetTemplateByTeam(ctx, db.GetTemplateByTeamParams{Name: name, TeamID: ac.TeamID}); err != nil {
		writeError(w, http.StatusNotFound, "not_found", "template not found")
		return
	}

	if err := h.deleteSnapshotBroadcast(ctx, name); err != nil {
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
