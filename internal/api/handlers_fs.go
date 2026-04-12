package api

import (
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/wrenn/internal/auth"
	"git.omukk.dev/wrenn/wrenn/internal/db"
	"git.omukk.dev/wrenn/wrenn/internal/id"
	"git.omukk.dev/wrenn/wrenn/internal/lifecycle"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

type fsHandler struct {
	db   *db.Queries
	pool *lifecycle.HostClientPool
}

func newFSHandler(db *db.Queries, pool *lifecycle.HostClientPool) *fsHandler {
	return &fsHandler{db: db, pool: pool}
}

type listDirRequest struct {
	Path  string `json:"path"`
	Depth uint32 `json:"depth"`
}

type fileEntryResponse struct {
	Name          string  `json:"name"`
	Path          string  `json:"path"`
	Type          string  `json:"type"`
	Size          int64   `json:"size"`
	Mode          uint32  `json:"mode"`
	Permissions   string  `json:"permissions"`
	Owner         string  `json:"owner"`
	Group         string  `json:"group"`
	ModifiedAt    int64   `json:"modified_at"`
	SymlinkTarget *string `json:"symlink_target,omitempty"`
}

type listDirResponse struct {
	Entries []fileEntryResponse `json:"entries"`
}

type makeDirRequest struct {
	Path string `json:"path"`
}

type makeDirResponse struct {
	Entry fileEntryResponse `json:"entry"`
}

type removeRequest struct {
	Path string `json:"path"`
}

// ListDir handles POST /v1/capsules/{id}/files/list.
func (h *fsHandler) ListDir(w http.ResponseWriter, r *http.Request) {
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

	var req listDirRequest
	if err := decodeJSON(r, &req); err != nil {
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

	resp, err := agent.ListDir(ctx, connect.NewRequest(&pb.ListDirRequest{
		SandboxId: sandboxIDStr,
		Path:      req.Path,
		Depth:     req.Depth,
	}))
	if err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	entries := make([]fileEntryResponse, 0, len(resp.Msg.Entries))
	for _, e := range resp.Msg.Entries {
		entries = append(entries, fileEntryFromPB(e))
	}

	writeJSON(w, http.StatusOK, listDirResponse{Entries: entries})
}

// MakeDir handles POST /v1/capsules/{id}/files/mkdir.
func (h *fsHandler) MakeDir(w http.ResponseWriter, r *http.Request) {
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

	var req makeDirRequest
	if err := decodeJSON(r, &req); err != nil {
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

	resp, err := agent.MakeDir(ctx, connect.NewRequest(&pb.MakeDirRequest{
		SandboxId: sandboxIDStr,
		Path:      req.Path,
	}))
	if err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, makeDirResponse{Entry: fileEntryFromPB(resp.Msg.Entry)})
}

// Remove handles POST /v1/capsules/{id}/files/remove.
func (h *fsHandler) Remove(w http.ResponseWriter, r *http.Request) {
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

	var req removeRequest
	if err := decodeJSON(r, &req); err != nil {
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

	if _, err := agent.RemovePath(ctx, connect.NewRequest(&pb.RemovePathRequest{
		SandboxId: sandboxIDStr,
		Path:      req.Path,
	})); err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func fileEntryFromPB(e *pb.FileEntry) fileEntryResponse {
	if e == nil {
		return fileEntryResponse{}
	}
	resp := fileEntryResponse{
		Name:        e.Name,
		Path:        e.Path,
		Type:        e.Type,
		Size:        e.Size,
		Mode:        e.Mode,
		Permissions: e.Permissions,
		Owner:       e.Owner,
		Group:       e.Group,
		ModifiedAt:  e.ModifiedAt,
	}
	if e.SymlinkTarget != nil {
		resp.SymlinkTarget = e.SymlinkTarget
	}
	return resp
}
