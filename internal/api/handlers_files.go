package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/wrenn/internal/auth"
	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/id"
	"git.omukk.dev/wrenn/wrenn/internal/lifecycle"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

type filesHandler struct {
	db   *db.Queries
	pool *lifecycle.HostClientPool
}

func newFilesHandler(db *db.Queries, pool *lifecycle.HostClientPool) *filesHandler {
	return &filesHandler{db: db, pool: pool}
}

// Upload handles POST /v1/sandboxes/{id}/files/write.
// Expects multipart/form-data with:
//   - "path" text field: absolute destination path inside the sandbox
//   - "file" file field: binary content to write
func (h *filesHandler) Upload(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sb.Status != "running" {
		writeError(w, http.StatusConflict, "invalid_state", "sandbox is not running")
		return
	}

	// Limit to 100 MB.
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)

	if err := r.ParseMultipartForm(100 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "too_large", "file exceeds 100 MB limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "expected multipart/form-data")
		return
	}

	filePath := r.FormValue("path")
	if filePath == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path field is required")
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "file field is required")
		return
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_error", "failed to read uploaded file")
		return
	}

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host_unavailable", "sandbox host is not reachable")
		return
	}

	if _, err := agent.WriteFile(ctx, connect.NewRequest(&pb.WriteFileRequest{
		SandboxId: sandboxIDStr,
		Path:      filePath,
		Content:   content,
	})); err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type readFileRequest struct {
	Path string `json:"path"`
}

// Download handles POST /v1/sandboxes/{id}/files/read.
// Accepts JSON body with path, returns raw file content with Content-Disposition.
func (h *filesHandler) Download(w http.ResponseWriter, r *http.Request) {
	sandboxIDStr := chi.URLParam(r, "id")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sandboxID, err := id.ParseSandboxID(sandboxIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid sandbox ID")
		return
	}

	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sb.Status != "running" {
		writeError(w, http.StatusConflict, "invalid_state", "sandbox is not running")
		return
	}

	var req readFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}

	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path is required")
		return
	}

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "host_unavailable", "sandbox host is not reachable")
		return
	}

	resp, err := agent.ReadFile(ctx, connect.NewRequest(&pb.ReadFileRequest{
		SandboxId: sandboxIDStr,
		Path:      req.Path,
	}))
	if err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(resp.Msg.Content)
}
