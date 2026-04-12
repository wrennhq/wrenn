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

	"git.omukk.dev/wrenn/wrenn/internal/audit"
	"git.omukk.dev/wrenn/wrenn/internal/auth"
	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/id"
	"git.omukk.dev/wrenn/wrenn/internal/lifecycle"
	"git.omukk.dev/wrenn/wrenn/internal/service"
	"git.omukk.dev/wrenn/wrenn/internal/validate"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

type adminCapsuleHandler struct {
	svc   *service.SandboxService
	db    *db.Queries
	pool  *lifecycle.HostClientPool
	audit *audit.AuditLogger
}

func newAdminCapsuleHandler(svc *service.SandboxService, db *db.Queries, pool *lifecycle.HostClientPool, al *audit.AuditLogger) *adminCapsuleHandler {
	return &adminCapsuleHandler{svc: svc, db: db, pool: pool, audit: al}
}

// Create handles POST /v1/admin/capsules.
func (h *adminCapsuleHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	ac := auth.MustFromContext(r.Context())

	sb, err := h.svc.Create(r.Context(), service.SandboxCreateParams{
		TeamID:     id.PlatformTeamID,
		Template:   req.Template,
		VCPUs:      req.VCPUs,
		MemoryMB:   req.MemoryMB,
		TimeoutSec: req.TimeoutSec,
	})
	if err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	h.audit.LogSandboxCreate(r.Context(), ac, sb.ID, sb.Template)
	writeJSON(w, http.StatusCreated, sandboxToResponse(sb))
}

// List handles GET /v1/admin/capsules.
func (h *adminCapsuleHandler) List(w http.ResponseWriter, r *http.Request) {
	sandboxes, err := h.svc.List(r.Context(), id.PlatformTeamID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "failed to list sandboxes")
		return
	}

	resp := make([]sandboxResponse, len(sandboxes))
	for i, sb := range sandboxes {
		resp[i] = sandboxToResponse(sb)
	}

	writeJSON(w, http.StatusOK, resp)
}

// Get handles GET /v1/admin/capsules/{id}.
func (h *adminCapsuleHandler) Get(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	sb, err := h.svc.Get(r.Context(), sandboxID, id.PlatformTeamID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}

	writeJSON(w, http.StatusOK, sandboxToResponse(sb))
}

// Destroy handles DELETE /v1/admin/capsules/{id}.
func (h *adminCapsuleHandler) Destroy(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	if err := h.svc.Destroy(r.Context(), sandboxID, id.PlatformTeamID); err != nil {
		status, code, msg := serviceErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	h.audit.LogSandboxDestroy(r.Context(), ac, sandboxID)
	w.WriteHeader(http.StatusNoContent)
}

type adminSnapshotRequest struct {
	Name string `json:"name"`
}

// Snapshot handles POST /v1/admin/capsules/{id}/snapshot.
// Pauses the capsule, takes a snapshot as a platform template, then destroys the capsule.
func (h *adminCapsuleHandler) Snapshot(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ac := auth.MustFromContext(r.Context())

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	var req adminSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
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

	// Verify sandbox exists and belongs to platform team BEFORE any
	// destructive operations (template overwrite).
	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: id.PlatformTeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sb.Status != "running" && sb.Status != "paused" {
		writeError(w, http.StatusConflict, "invalid_state", "sandbox must be running or paused")
		return
	}

	// Check if name already exists as a platform template.
	if existing, err := h.db.GetPlatformTemplateByName(ctx, req.Name); err == nil {
		// Delete old snapshot files from all hosts before removing the DB record.
		if err := deleteSnapshotBroadcast(ctx, h.db, h.pool, existing.TeamID, existing.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "agent_error", "failed to delete existing snapshot files")
			return
		}
		if err := h.db.DeleteTemplateByTeam(ctx, db.DeleteTemplateByTeamParams{Name: req.Name, TeamID: id.PlatformTeamID}); err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "failed to remove existing template record")
			return
		}
	}

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host_unavailable", "sandbox host is not reachable")
		return
	}

	// Pre-mark sandbox as "paused" to prevent the reconciler from racing.
	if sb.Status == "running" {
		if _, err := h.db.UpdateSandboxStatus(ctx, db.UpdateSandboxStatusParams{
			ID: sandboxID, Status: "paused",
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "db_error", "failed to update sandbox status")
			return
		}
	}

	// Use a detached context so the snapshot completes even if the client disconnects.
	snapCtx, snapCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer snapCancel()

	newTemplateID := id.NewTemplateID()

	resp, err := agent.CreateSnapshot(snapCtx, connect.NewRequest(&pb.CreateSnapshotRequest{
		SandboxId:  sandboxIDStr,
		Name:       req.Name,
		TeamId:     formatUUIDForRPC(id.PlatformTeamID),
		TemplateId: formatUUIDForRPC(newTemplateID),
	}))
	if err != nil {
		// Snapshot failed — revert status.
		if sb.Status == "running" {
			if _, dbErr := h.db.UpdateSandboxStatus(snapCtx, db.UpdateSandboxStatusParams{
				ID: sandboxID, Status: "running",
			}); dbErr != nil {
				slog.Error("failed to revert sandbox status after snapshot error", "sandbox_id", sandboxIDStr, "error", dbErr)
			}
		}
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	tmpl, err := h.db.InsertTemplate(snapCtx, db.InsertTemplateParams{
		ID:          newTemplateID,
		Name:        req.Name,
		Type:        "snapshot",
		Vcpus:       sb.Vcpus,
		MemoryMb:    sb.MemoryMb,
		SizeBytes:   resp.Msg.SizeBytes,
		TeamID:      id.PlatformTeamID,
		DefaultUser: "root",
		DefaultEnv:  []byte("{}"),
	})
	if err != nil {
		slog.Error("failed to insert template record", "name", req.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "db_error", "snapshot created but failed to record in database")
		return
	}

	// Destroy the ephemeral capsule after successful snapshot.
	if err := h.svc.Destroy(snapCtx, sandboxID, id.PlatformTeamID); err != nil {
		slog.Error("failed to destroy capsule after snapshot", "sandbox_id", sandboxIDStr, "error", err)
		// Don't fail the response — the snapshot was created successfully.
	}

	h.audit.LogSnapshotCreate(snapCtx, ac, req.Name)

	if ctx.Err() != nil {
		slog.Info("snapshot created but client disconnected before response", "name", req.Name)
		return
	}
	writeJSON(w, http.StatusCreated, templateToResponse(tmpl))
}
