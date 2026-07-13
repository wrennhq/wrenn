package api

import (
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"

	"connectrpc.com/connect"

	"git.omukk.dev/wrenn/wrenn/pkg/apperr"
	"git.omukk.dev/wrenn/wrenn/pkg/auth"
	"git.omukk.dev/wrenn/wrenn/pkg/db"
	"git.omukk.dev/wrenn/wrenn/pkg/lifecycle"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
)

type filesStreamHandler struct {
	db   *db.Queries
	pool *lifecycle.HostClientPool
}

func newFilesStreamHandler(db *db.Queries, pool *lifecycle.HostClientPool) *filesStreamHandler {
	return &filesStreamHandler{db: db, pool: pool}
}

// StreamUpload handles POST /v1/capsules/{id}/files/stream/write.
// Expects multipart/form-data with "path" text field and "file" file field.
// Streams file content directly from the request body to the host agent without buffering.
func (h *filesStreamHandler) StreamUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, _, sandboxIDStr, ok := requireRunningSandbox(w, r, h.db, ac.TeamID)
	if !ok {
		return
	}

	// Parse boundary from Content-Type without buffering the body.
	contentType := r.Header.Get("Content-Type")
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil || params["boundary"] == "" {
		writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Expected multipart/form-data with a boundary."))
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
			writeErr(w, r, apperr.InvalidRequest.WrapMsg(err, "Malformed multipart request body."))
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
		writeErr(w, r, apperr.ValidationFailed.Msg("The path field is required.").With("field", "path"))
		return
	}
	if filePart == nil {
		writeErr(w, r, apperr.ValidationFailed.Msg("The file field is required.").With("field", "file"))
		return
	}
	defer filePart.Close()

	agent, err := agentForHost(ctx, h.db, h.pool, sb.HostID)
	if err != nil {
		writeErr(w, r, apperr.HostUnreachable.Wrap(err))
		return
	}

	// Open client-streaming RPC to host agent.
	stream := agent.WriteFileStream(ctx)
	var streamClosed bool
	defer func() {
		if !streamClosed {
			_, _ = stream.CloseAndReceive()
		}
	}()

	// Send metadata first.
	if err := stream.Send(&pb.WriteFileStreamRequest{
		Content: &pb.WriteFileStreamRequest_Meta{
			Meta: &pb.WriteFileStreamMeta{
				SandboxId: sandboxIDStr,
				Path:      filePath,
			},
		},
	}); err != nil {
		writeErr(w, r, apperr.HostUnreachable.Wrap(err))
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
				writeErr(w, r, apperr.HostUnreachable.Wrap(sendErr))
				return
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, r, apperr.Internal.WrapMsg(err, "Failed to read the uploaded file."))
			return
		}
	}

	// Close and receive response.
	streamClosed = true
	if _, err := stream.CloseAndReceive(); err != nil {
		writeErr(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// StreamDownload handles POST /v1/capsules/{id}/files/stream/read.
// Accepts JSON body with path, streams file content back without buffering.
func (h *filesStreamHandler) StreamDownload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ac := auth.MustFromContext(ctx)

	sb, _, sandboxIDStr, ok := requireRunningSandbox(w, r, h.db, ac.TeamID)
	if !ok {
		return
	}

	var req readFileRequest
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

	// Open server-streaming RPC to host agent.
	stream, err := agent.ReadFileStream(ctx, connect.NewRequest(&pb.ReadFileStreamRequest{
		SandboxId: sandboxIDStr,
		Path:      req.Path,
	}))
	if err != nil {
		writeErr(w, r, err)
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
