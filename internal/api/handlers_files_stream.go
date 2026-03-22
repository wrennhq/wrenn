package api

import (
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"

	"git.omukk.dev/wrenn/sandbox/internal/auth"
	"git.omukk.dev/wrenn/sandbox/internal/db"
	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"
)

type filesStreamHandler struct {
	db    *db.Queries
	agent hostagentv1connect.HostAgentServiceClient
}

func newFilesStreamHandler(db *db.Queries, agent hostagentv1connect.HostAgentServiceClient) *filesStreamHandler {
	return &filesStreamHandler{db: db, agent: agent}
}

// StreamUpload handles POST /v1/sandboxes/{id}/files/stream/write.
// Expects multipart/form-data with "path" text field and "file" file field.
// Streams file content directly from the request body to the host agent without buffering.
func (h *filesStreamHandler) StreamUpload(w http.ResponseWriter, r *http.Request) {
	sandboxID := chi.URLParam(r, "id")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, err := h.db.GetSandboxByTeam(ctx, db.GetSandboxByTeamParams{ID: sandboxID, TeamID: ac.TeamID})
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "sandbox not found")
		return
	}
	if sb.Status != "running" {
		writeError(w, http.StatusConflict, "invalid_state", "sandbox is not running")
		return
	}

	// Parse boundary from Content-Type without buffering the body.
	contentType := r.Header.Get("Content-Type")
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "expected multipart/form-data with boundary")
		return
	}

	// Read parts manually from the multipart stream.
	mr := multipart.NewReader(r.Body, params["boundary"])

	var filePath string
	var filePart *multipart.Part

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "failed to parse multipart")
			return
		}
		switch part.FormName() {
		case "path":
			data, _ := io.ReadAll(part)
			filePath = string(data)
		case "file":
			filePart = part
		}
		if filePath != "" && filePart != nil {
			break
		}
	}

	if filePath == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path field is required")
		return
	}
	if filePart == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "file field is required")
		return
	}
	defer filePart.Close()

	// Open client-streaming RPC to host agent.
	stream := h.agent.WriteFileStream(ctx)

	// Send metadata first.
	if err := stream.Send(&pb.WriteFileStreamRequest{
		Content: &pb.WriteFileStreamRequest_Meta{
			Meta: &pb.WriteFileStreamMeta{
				SandboxId: sandboxID,
				Path:      filePath,
			},
		},
	}); err != nil {
		writeError(w, http.StatusBadGateway, "agent_error", "failed to send file metadata")
		return
	}

	// Stream file content in 64KB chunks directly from the multipart part.
	buf := make([]byte, 64*1024)
	for {
		n, err := filePart.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if sendErr := stream.Send(&pb.WriteFileStreamRequest{
				Content: &pb.WriteFileStreamRequest_Chunk{Chunk: chunk},
			}); sendErr != nil {
				writeError(w, http.StatusBadGateway, "agent_error", "failed to stream file chunk")
				return
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_error", "failed to read uploaded file")
			return
		}
	}

	// Close and receive response.
	if _, err := stream.CloseAndReceive(); err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// StreamDownload handles POST /v1/sandboxes/{id}/files/stream/read.
// Accepts JSON body with path, streams file content back without buffering.
func (h *filesStreamHandler) StreamDownload(w http.ResponseWriter, r *http.Request) {
	sandboxID := chi.URLParam(r, "id")
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

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
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "path is required")
		return
	}

	// Open server-streaming RPC to host agent.
	stream, err := h.agent.ReadFileStream(ctx, connect.NewRequest(&pb.ReadFileStreamRequest{
		SandboxId: sandboxID,
		Path:      req.Path,
	}))
	if err != nil {
		status, code, msg := agentErrToHTTP(err)
		writeError(w, status, code, msg)
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "application/octet-stream")

	flusher, canFlush := w.(http.Flusher)
	for stream.Receive() {
		chunk := stream.Msg().Chunk
		if len(chunk) > 0 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
	}

	if err := stream.Err(); err != nil {
		// Headers already sent, nothing we can do but log.
		slog.Warn("file stream error after headers sent", "error", err)
	}
}
