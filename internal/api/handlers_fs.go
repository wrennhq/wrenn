package api

import (
	"net/http"

	"connectrpc.com/connect"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
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
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, _, sandboxIDStr, ok := requireRunningSandbox(w, r, h.db, ac.TeamID)
	if !ok {
		return
	}

	var req listDirRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}
	if req.Path == "" {
		writeErr(w, r, apperr.ValidationFailed.Msg("The path field is required.").With("field", "path"))
		return
	}

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeErr(w, r, apperr.HostUnreachable.Wrap(err))
		return
	}

	resp, err := agent.ListDir(ctx, connect.NewRequest(&pb.ListDirRequest{
		SandboxId: sandboxIDStr,
		Path:      req.Path,
		Depth:     req.Depth,
	}))
	if err != nil {
		writeErr(w, r, err)
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
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, _, sandboxIDStr, ok := requireRunningSandbox(w, r, h.db, ac.TeamID)
	if !ok {
		return
	}

	var req makeDirRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}
	if req.Path == "" {
		writeErr(w, r, apperr.ValidationFailed.Msg("The path field is required.").With("field", "path"))
		return
	}

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeErr(w, r, apperr.HostUnreachable.Wrap(err))
		return
	}

	resp, err := agent.MakeDir(ctx, connect.NewRequest(&pb.MakeDirRequest{
		SandboxId: sandboxIDStr,
		Path:      req.Path,
	}))
	if err != nil {
		writeErr(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, makeDirResponse{Entry: fileEntryFromPB(resp.Msg.Entry)})
}

// Remove handles POST /v1/capsules/{id}/files/remove.
func (h *fsHandler) Remove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, _, sandboxIDStr, ok := requireRunningSandbox(w, r, h.db, ac.TeamID)
	if !ok {
		return
	}

	var req removeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Invalid JSON body."))
		return
	}
	if req.Path == "" {
		writeErr(w, r, apperr.ValidationFailed.Msg("The path field is required.").With("field", "path"))
		return
	}

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeErr(w, r, apperr.HostUnreachable.Wrap(err))
		return
	}

	if _, err := agent.RemovePath(ctx, connect.NewRequest(&pb.RemovePathRequest{
		SandboxId: sandboxIDStr,
		Path:      req.Path,
	})); err != nil {
		writeErr(w, r, err)
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
