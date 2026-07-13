package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"connectrpc.com/connect"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

type filesHandler struct {
	db   *db.Queries
	pool *lifecycle.HostClientPool
}

func newFilesHandler(db *db.Queries, pool *lifecycle.HostClientPool) *filesHandler {
	return &filesHandler{db: db, pool: pool}
}

// Upload handles POST /v1/capsules/{id}/files/write.
// Expects multipart/form-data with:
//   - "path" text field: absolute destination path inside the sandbox
//   - "file" file field: binary content to write
func (h *filesHandler) Upload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, _, sandboxIDStr, ok := requireRunningSandbox(w, r, h.db, ac.TeamID)
	if !ok {
		return
	}

	// Limit to 100 MB.
	r.Body = http.MaxBytesReader(w, r.Body, 100<<20)

	if err := r.ParseMultipartForm(100 << 20); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeErr(w, r, apperr.PayloadTooLarge.Msg("File exceeds the 100 MB limit."))
			return
		}
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Expected multipart/form-data."))
		return
	}

	filePath := r.FormValue("path")
	if filePath == "" {
		writeErr(w, r, apperr.ValidationFailed.Msg("The path field is required.").With("field", "path"))
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, r, apperr.ValidationFailed.Msg("The file field is required.").With("field", "file"))
		return
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, r, apperr.Internal.WrapMsg(err, "Failed to read the uploaded file."))
		return
	}

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeErr(w, r, apperr.HostUnreachable.Wrap(err))
		return
	}

	if _, err := agent.WriteFile(ctx, connect.NewRequest(&pb.WriteFileRequest{
		SandboxId: sandboxIDStr,
		Path:      filePath,
		Content:   content,
	})); err != nil {
		writeErr(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type readFileRequest struct {
	Path string `json:"path"`
}

// Download handles POST /v1/capsules/{id}/files/read.
// Accepts JSON body with path, returns raw file content with Content-Disposition.
func (h *filesHandler) Download(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, _, sandboxIDStr, ok := requireRunningSandbox(w, r, h.db, ac.TeamID)
	if !ok {
		return
	}

	var req readFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	resp, err := agent.ReadFile(ctx, connect.NewRequest(&pb.ReadFileRequest{
		SandboxId: sandboxIDStr,
		Path:      req.Path,
	}))
	if err != nil {
		writeErr(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(resp.Msg.Content)
}
