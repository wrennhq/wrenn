package hostagent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	envdpb "git.omukk.dev/wrenn/wrenn/proto/envd/gen"
	pb "git.omukk.dev/wrenn/wrenn/proto/hostagent/gen"
	"git.omukk.dev/wrenn/wrenn/proto/hostagent/gen/hostagentv1connect"

	"git.omukk.dev/wrenn/wrenn/internal/sandbox"
)

// Server implements the HostAgentService Connect RPC handler.
type Server struct {
	hostagentv1connect.UnimplementedHostAgentServiceHandler
	mgr       *sandbox.Manager
	terminate func() // called when the CP requests agent termination
}

// NewServer creates a new host agent RPC server.
// terminate is invoked (in a goroutine) when the CP calls the Terminate RPC,
// allowing main to perform a clean shutdown.
func NewServer(mgr *sandbox.Manager, terminate func()) *Server {
	return &Server{mgr: mgr, terminate: terminate}
}

// parseUUIDString parses a UUID hex string into a pgtype.UUID.
// An empty string yields an all-zeros UUID (valid).
func parseUUIDString(s string) (pgtype.UUID, error) {
	if s == "" {
		return pgtype.UUID{Bytes: [16]byte{}, Valid: true}, nil
	}
	parsed, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid UUID %q: %w", s, err)
	}
	return pgtype.UUID{Bytes: parsed, Valid: true}, nil
}

func (s *Server) CreateSandbox(
	ctx context.Context,
	req *connect.Request[pb.CreateSandboxRequest],
) (*connect.Response[pb.CreateSandboxResponse], error) {
	msg := req.Msg

	teamID, err := parseUUIDString(msg.TeamId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	templateID, err := parseUUIDString(msg.TemplateId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	sb, err := s.mgr.Create(ctx, msg.SandboxId, teamID, templateID, int(msg.Vcpus), int(msg.MemoryMb), int(msg.TimeoutSec), int(msg.DiskSizeMb))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create sandbox: %w", err))
	}

	// Apply template defaults (user, env vars) if provided.
	if msg.DefaultUser != "" || len(msg.DefaultEnv) > 0 {
		if err := s.mgr.SetDefaults(ctx, sb.ID, msg.DefaultUser, msg.DefaultEnv); err != nil {
			slog.Warn("failed to set sandbox defaults", "sandbox", sb.ID, "error", err)
		}
	}

	return connect.NewResponse(&pb.CreateSandboxResponse{
		SandboxId: sb.ID,
		Status:    string(sb.Status),
		HostIp:    sb.HostIP.String(),
		Metadata:  sb.Metadata,
	}), nil
}

func (s *Server) DestroySandbox(
	ctx context.Context,
	req *connect.Request[pb.DestroySandboxRequest],
) (*connect.Response[pb.DestroySandboxResponse], error) {
	if err := s.mgr.Destroy(ctx, req.Msg.SandboxId); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(&pb.DestroySandboxResponse{}), nil
}

func (s *Server) PauseSandbox(
	ctx context.Context,
	req *connect.Request[pb.PauseSandboxRequest],
) (*connect.Response[pb.PauseSandboxResponse], error) {
	if err := s.mgr.Pause(ctx, req.Msg.SandboxId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pb.PauseSandboxResponse{}), nil
}

func (s *Server) ResumeSandbox(
	ctx context.Context,
	req *connect.Request[pb.ResumeSandboxRequest],
) (*connect.Response[pb.ResumeSandboxResponse], error) {
	msg := req.Msg
	sb, err := s.mgr.Resume(ctx, msg.SandboxId, int(msg.TimeoutSec), msg.KernelVersion, msg.DefaultUser, msg.DefaultEnv)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pb.ResumeSandboxResponse{
		SandboxId: sb.ID,
		Status:    string(sb.Status),
		HostIp:    sb.HostIP.String(),
		Metadata:  sb.Metadata,
	}), nil
}

func (s *Server) CreateSnapshot(
	ctx context.Context,
	req *connect.Request[pb.CreateSnapshotRequest],
) (*connect.Response[pb.CreateSnapshotResponse], error) {
	msg := req.Msg
	teamID, err := parseUUIDString(msg.TeamId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	templateID, err := parseUUIDString(msg.TemplateId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	sizeBytes, err := s.mgr.CreateSnapshot(ctx, msg.SandboxId, teamID, templateID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create snapshot: %w", err))
	}
	return connect.NewResponse(&pb.CreateSnapshotResponse{
		SizeBytes: sizeBytes,
	}), nil
}

func (s *Server) DeleteSnapshot(
	ctx context.Context,
	req *connect.Request[pb.DeleteSnapshotRequest],
) (*connect.Response[pb.DeleteSnapshotResponse], error) {
	msg := req.Msg
	teamID, err := parseUUIDString(msg.TeamId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	templateID, err := parseUUIDString(msg.TemplateId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if err := s.mgr.DeleteSnapshot(teamID, templateID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("delete snapshot: %w", err))
	}
	return connect.NewResponse(&pb.DeleteSnapshotResponse{}), nil
}

func (s *Server) FlattenRootfs(
	ctx context.Context,
	req *connect.Request[pb.FlattenRootfsRequest],
) (*connect.Response[pb.FlattenRootfsResponse], error) {
	msg := req.Msg
	teamID, err := parseUUIDString(msg.TeamId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	templateID, err := parseUUIDString(msg.TemplateId)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	sizeBytes, err := s.mgr.FlattenRootfs(ctx, msg.SandboxId, teamID, templateID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("flatten rootfs: %w", err))
	}
	return connect.NewResponse(&pb.FlattenRootfsResponse{
		SizeBytes: sizeBytes,
	}), nil
}

func (s *Server) PingSandbox(
	ctx context.Context,
	req *connect.Request[pb.PingSandboxRequest],
) (*connect.Response[pb.PingSandboxResponse], error) {
	if err := s.mgr.Ping(req.Msg.SandboxId); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&pb.PingSandboxResponse{}), nil
}

func (s *Server) Exec(
	ctx context.Context,
	req *connect.Request[pb.ExecRequest],
) (*connect.Response[pb.ExecResponse], error) {
	msg := req.Msg

	timeout := 30 * time.Second
	if msg.TimeoutSec > 0 {
		timeout = time.Duration(msg.TimeoutSec) * time.Second
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := s.mgr.Exec(execCtx, msg.SandboxId, msg.Cmd, msg.Args...)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("exec: %w", err))
	}

	return connect.NewResponse(&pb.ExecResponse{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
	}), nil
}

func (s *Server) WriteFile(
	ctx context.Context,
	req *connect.Request[pb.WriteFileRequest],
) (*connect.Response[pb.WriteFileResponse], error) {
	msg := req.Msg

	client, err := s.mgr.GetClient(msg.SandboxId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	if err := client.WriteFile(ctx, msg.Path, msg.Content); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("write file: %w", err))
	}

	return connect.NewResponse(&pb.WriteFileResponse{}), nil
}

func (s *Server) ReadFile(
	ctx context.Context,
	req *connect.Request[pb.ReadFileRequest],
) (*connect.Response[pb.ReadFileResponse], error) {
	msg := req.Msg

	client, err := s.mgr.GetClient(msg.SandboxId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	content, err := client.ReadFile(ctx, msg.Path)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("read file: %w", err))
	}

	return connect.NewResponse(&pb.ReadFileResponse{Content: content}), nil
}

func (s *Server) ListDir(
	ctx context.Context,
	req *connect.Request[pb.ListDirRequest],
) (*connect.Response[pb.ListDirResponse], error) {
	msg := req.Msg

	client, err := s.mgr.GetClient(msg.SandboxId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	resp, err := client.ListDir(ctx, msg.Path, msg.Depth)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list dir: %w", err))
	}

	entries := make([]*pb.FileEntry, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		entries = append(entries, entryInfoToPB(e))
	}

	return connect.NewResponse(&pb.ListDirResponse{Entries: entries}), nil
}

func (s *Server) MakeDir(
	ctx context.Context,
	req *connect.Request[pb.MakeDirRequest],
) (*connect.Response[pb.MakeDirResponse], error) {
	msg := req.Msg

	client, err := s.mgr.GetClient(msg.SandboxId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	resp, err := client.MakeDir(ctx, msg.Path)
	if err != nil {
		return nil, fmt.Errorf("make dir: %w", err)
	}

	return connect.NewResponse(&pb.MakeDirResponse{
		Entry: entryInfoToPB(resp.Entry),
	}), nil
}

func (s *Server) RemovePath(
	ctx context.Context,
	req *connect.Request[pb.RemovePathRequest],
) (*connect.Response[pb.RemovePathResponse], error) {
	msg := req.Msg

	client, err := s.mgr.GetClient(msg.SandboxId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	if err := client.Remove(ctx, msg.Path); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("remove: %w", err))
	}

	return connect.NewResponse(&pb.RemovePathResponse{}), nil
}

func (s *Server) ExecStream(
	ctx context.Context,
	req *connect.Request[pb.ExecStreamRequest],
	stream *connect.ServerStream[pb.ExecStreamResponse],
) error {
	msg := req.Msg

	// Only apply a timeout if explicitly requested; streaming execs may be long-running.
	execCtx := ctx
	if msg.TimeoutSec > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(msg.TimeoutSec)*time.Second)
		defer cancel()
	}

	events, err := s.mgr.ExecStream(execCtx, msg.SandboxId, msg.Cmd, msg.Args...)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("exec stream: %w", err))
	}

	for ev := range events {
		var resp pb.ExecStreamResponse
		switch ev.Type {
		case "start":
			resp.Event = &pb.ExecStreamResponse_Start{
				Start: &pb.ExecStreamStart{Pid: ev.PID},
			}
		case "stdout":
			resp.Event = &pb.ExecStreamResponse_Data{
				Data: &pb.ExecStreamData{
					Output: &pb.ExecStreamData_Stdout{Stdout: ev.Data},
				},
			}
		case "stderr":
			resp.Event = &pb.ExecStreamResponse_Data{
				Data: &pb.ExecStreamData{
					Output: &pb.ExecStreamData_Stderr{Stderr: ev.Data},
				},
			}
		case "end":
			resp.Event = &pb.ExecStreamResponse_End{
				End: &pb.ExecStreamEnd{
					ExitCode: ev.ExitCode,
					Error:    ev.Error,
				},
			}
		}
		if err := stream.Send(&resp); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) WriteFileStream(
	ctx context.Context,
	stream *connect.ClientStream[pb.WriteFileStreamRequest],
) (*connect.Response[pb.WriteFileStreamResponse], error) {
	// First message must contain metadata.
	if !stream.Receive() {
		if err := stream.Err(); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("empty stream"))
	}

	first := stream.Msg()
	meta := first.GetMeta()
	if meta == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("first message must contain metadata"))
	}

	client, err := s.mgr.GetClient(meta.SandboxId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}

	// Use io.Pipe to stream chunks into a multipart body for envd's REST endpoint.
	pr, pw := io.Pipe()
	mpWriter := multipart.NewWriter(pw)

	// Write multipart data in a goroutine.
	errCh := make(chan error, 1)
	go func() {
		defer pw.Close()
		part, err := mpWriter.CreateFormFile("file", "upload")
		if err != nil {
			errCh <- fmt.Errorf("create multipart: %w", err)
			return
		}

		for stream.Receive() {
			chunk := stream.Msg().GetChunk()
			if len(chunk) == 0 {
				continue
			}
			if _, err := part.Write(chunk); err != nil {
				errCh <- fmt.Errorf("write chunk: %w", err)
				return
			}
		}
		if err := stream.Err(); err != nil {
			errCh <- err
			return
		}
		mpWriter.Close()
		errCh <- nil
	}()

	// Send the streaming multipart body to envd.
	base := client.BaseURL()
	u := fmt.Sprintf("%s/files?%s", base, url.Values{
		"path":     {meta.Path},
		"username": {"root"},
	}.Encode())

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, pr)
	if err != nil {
		pw.CloseWithError(err)
		<-errCh
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create request: %w", err))
	}
	httpReq.Header.Set("Content-Type", mpWriter.FormDataContentType())

	resp, err := client.StreamingHTTPClient().Do(httpReq)
	if err != nil {
		pw.CloseWithError(err)
		<-errCh
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("write file stream: %w", err))
	}
	defer resp.Body.Close()

	// Wait for the writer goroutine.
	if writerErr := <-errCh; writerErr != nil {
		return nil, connect.NewError(connect.CodeInternal, writerErr)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("envd write: status %d: %s", resp.StatusCode, string(body)))
	}

	slog.Debug("streaming file write complete", "sandbox_id", meta.SandboxId, "path", meta.Path)
	return connect.NewResponse(&pb.WriteFileStreamResponse{}), nil
}

func (s *Server) ReadFileStream(
	ctx context.Context,
	req *connect.Request[pb.ReadFileStreamRequest],
	stream *connect.ServerStream[pb.ReadFileStreamResponse],
) error {
	msg := req.Msg

	client, err := s.mgr.GetClient(msg.SandboxId)
	if err != nil {
		return connect.NewError(connect.CodeNotFound, err)
	}

	base := client.BaseURL()
	u := fmt.Sprintf("%s/files?%s", base, url.Values{
		"path":     {msg.Path},
		"username": {"root"},
	}.Encode())

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("create request: %w", err))
	}

	resp, err := client.StreamingHTTPClient().Do(httpReq)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("read file stream: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return connect.NewError(connect.CodeInternal, fmt.Errorf("envd read: status %d: %s", resp.StatusCode, string(body)))
	}

	// Stream file content in 64KB chunks.
	buf := make([]byte, 64*1024)
	for {
		// Bail out early if the client disconnected or the context was cancelled.
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return connect.NewError(connect.CodeDeadlineExceeded, ctx.Err())
			}
			return connect.NewError(connect.CodeCanceled, ctx.Err())
		default:
		}

		n, err := resp.Body.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if sendErr := stream.Send(&pb.ReadFileStreamResponse{Chunk: chunk}); sendErr != nil {
				return sendErr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return connect.NewError(connect.CodeInternal, fmt.Errorf("read body: %w", err))
		}
	}

	return nil
}

func (s *Server) ListSandboxes(
	ctx context.Context,
	req *connect.Request[pb.ListSandboxesRequest],
) (*connect.Response[pb.ListSandboxesResponse], error) {
	sandboxes := s.mgr.List()

	infos := make([]*pb.SandboxInfo, len(sandboxes))
	for i, sb := range sandboxes {
		infos[i] = &pb.SandboxInfo{
			SandboxId:        sb.ID,
			Status:           string(sb.Status),
			TeamId:           uuid.UUID(sb.TemplateTeamID).String(),
			TemplateId:       uuid.UUID(sb.TemplateID).String(),
			Vcpus:            int32(sb.VCPUs),
			MemoryMb:         int32(sb.MemoryMB),
			HostIp:           sb.HostIP.String(),
			CreatedAtUnix:    sb.CreatedAt.Unix(),
			LastActiveAtUnix: sb.LastActiveAt.Unix(),
			TimeoutSec:       int32(sb.TimeoutSec),
			Metadata:         sb.Metadata,
		}
	}

	return connect.NewResponse(&pb.ListSandboxesResponse{
		Sandboxes:            infos,
		AutoPausedSandboxIds: s.mgr.DrainAutoPausedIDs(),
	}), nil
}

func (s *Server) Terminate(
	_ context.Context,
	_ *connect.Request[pb.TerminateRequest],
) (*connect.Response[pb.TerminateResponse], error) {
	slog.Info("terminate RPC received — scheduling shutdown")
	if s.terminate != nil {
		go s.terminate()
	}
	return connect.NewResponse(&pb.TerminateResponse{}), nil
}

func (s *Server) GetSandboxMetrics(
	_ context.Context,
	req *connect.Request[pb.GetSandboxMetricsRequest],
) (*connect.Response[pb.GetSandboxMetricsResponse], error) {
	msg := req.Msg

	points, err := s.mgr.GetMetrics(msg.SandboxId, msg.Range)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		if strings.Contains(err.Error(), "invalid range") {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pb.GetSandboxMetricsResponse{Points: metricPointsToPB(points)}), nil
}

func (s *Server) FlushSandboxMetrics(
	_ context.Context,
	req *connect.Request[pb.FlushSandboxMetricsRequest],
) (*connect.Response[pb.FlushSandboxMetricsResponse], error) {
	pts10m, pts2h, pts24h, err := s.mgr.FlushMetrics(req.Msg.SandboxId)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&pb.FlushSandboxMetricsResponse{
		Points_10M: metricPointsToPB(pts10m),
		Points_2H:  metricPointsToPB(pts2h),
		Points_24H: metricPointsToPB(pts24h),
	}), nil
}

func metricPointsToPB(pts []sandbox.MetricPoint) []*pb.MetricPoint {
	out := make([]*pb.MetricPoint, len(pts))
	for i, p := range pts {
		out[i] = &pb.MetricPoint{
			TimestampUnix: p.Timestamp.Unix(),
			CpuPct:        p.CPUPct,
			MemBytes:      p.MemBytes,
			DiskBytes:     p.DiskBytes,
		}
	}
	return out
}

func (s *Server) PtyAttach(
	ctx context.Context,
	req *connect.Request[pb.PtyAttachRequest],
	stream *connect.ServerStream[pb.PtyAttachResponse],
) error {
	msg := req.Msg

	events, err := s.mgr.PtyAttach(ctx, msg.SandboxId, msg.Tag, msg.Cmd, msg.Args, msg.Cols, msg.Rows, msg.Envs, msg.Cwd)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("pty attach: %w", err))
	}

	for ev := range events {
		var resp pb.PtyAttachResponse
		switch ev.Type {
		case "started":
			resp.Event = &pb.PtyAttachResponse_Started{
				Started: &pb.PtyStarted{Pid: ev.PID, Tag: msg.Tag},
			}
		case "output":
			resp.Event = &pb.PtyAttachResponse_Output{
				Output: &pb.PtyOutput{Data: ev.Data},
			}
		case "end":
			resp.Event = &pb.PtyAttachResponse_Exited{
				Exited: &pb.PtyExited{ExitCode: ev.ExitCode, Error: ev.Error},
			}
		default:
			continue
		}
		if err := stream.Send(&resp); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) PtySendInput(
	ctx context.Context,
	req *connect.Request[pb.PtySendInputRequest],
) (*connect.Response[pb.PtySendInputResponse], error) {
	msg := req.Msg

	if err := s.mgr.PtySendInput(ctx, msg.SandboxId, msg.Tag, msg.Data); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("pty send input: %w", err))
	}

	return connect.NewResponse(&pb.PtySendInputResponse{}), nil
}

func (s *Server) PtyResize(
	ctx context.Context,
	req *connect.Request[pb.PtyResizeRequest],
) (*connect.Response[pb.PtyResizeResponse], error) {
	msg := req.Msg

	if err := s.mgr.PtyResize(ctx, msg.SandboxId, msg.Tag, msg.Cols, msg.Rows); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("pty resize: %w", err))
	}

	return connect.NewResponse(&pb.PtyResizeResponse{}), nil
}

func (s *Server) PtyKill(
	ctx context.Context,
	req *connect.Request[pb.PtyKillRequest],
) (*connect.Response[pb.PtyKillResponse], error) {
	msg := req.Msg

	if err := s.mgr.PtyKill(ctx, msg.SandboxId, msg.Tag); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("pty kill: %w", err))
	}

	return connect.NewResponse(&pb.PtyKillResponse{}), nil
}

// entryInfoToPB maps an envd EntryInfo to a hostagent FileEntry.
func entryInfoToPB(e *envdpb.EntryInfo) *pb.FileEntry {
	if e == nil {
		return nil
	}

	var fileType string
	switch e.Type {
	case envdpb.FileType_FILE_TYPE_FILE:
		fileType = "file"
	case envdpb.FileType_FILE_TYPE_DIRECTORY:
		fileType = "directory"
	case envdpb.FileType_FILE_TYPE_SYMLINK:
		fileType = "symlink"
	default:
		fileType = "unknown"
	}

	entry := &pb.FileEntry{
		Name:        e.Name,
		Path:        e.Path,
		Type:        fileType,
		Size:        e.Size,
		Mode:        e.Mode,
		Permissions: e.Permissions,
		Owner:       e.Owner,
		Group:       e.Group,
	}

	if e.ModifiedTime != nil {
		entry.ModifiedAt = e.ModifiedTime.GetSeconds()
	}

	if e.SymlinkTarget != nil {
		entry.SymlinkTarget = e.SymlinkTarget
	}

	return entry
}

// ── Background Processes ────────────────────────────────────────────

func (s *Server) StartBackground(
	ctx context.Context,
	req *connect.Request[pb.StartBackgroundRequest],
) (*connect.Response[pb.StartBackgroundResponse], error) {
	msg := req.Msg

	pid, err := s.mgr.StartBackground(ctx, msg.SandboxId, msg.Tag, msg.Cmd, msg.Args, msg.Envs, msg.Cwd)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("start background: %w", err))
	}

	return connect.NewResponse(&pb.StartBackgroundResponse{
		Pid: pid,
		Tag: msg.Tag,
	}), nil
}

func (s *Server) ListProcesses(
	ctx context.Context,
	req *connect.Request[pb.ListProcessesRequest],
) (*connect.Response[pb.ListProcessesResponse], error) {
	procs, err := s.mgr.ListProcesses(ctx, req.Msg.SandboxId)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list processes: %w", err))
	}

	entries := make([]*pb.ProcessEntry, 0, len(procs))
	for _, p := range procs {
		entries = append(entries, &pb.ProcessEntry{
			Pid:  p.PID,
			Tag:  p.Tag,
			Cmd:  p.Cmd,
			Args: p.Args,
		})
	}

	return connect.NewResponse(&pb.ListProcessesResponse{
		Processes: entries,
	}), nil
}

func (s *Server) KillProcess(
	ctx context.Context,
	req *connect.Request[pb.KillProcessRequest],
) (*connect.Response[pb.KillProcessResponse], error) {
	msg := req.Msg

	// Resolve PID/tag selector.
	var pid uint32
	var tag string
	switch sel := msg.Selector.(type) {
	case *pb.KillProcessRequest_Pid:
		pid = sel.Pid
	case *pb.KillProcessRequest_Tag:
		tag = sel.Tag
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("pid or tag is required"))
	}

	// Map signal string to envd enum.
	var signal envdpb.Signal
	switch msg.Signal {
	case "", "SIGKILL":
		signal = envdpb.Signal_SIGNAL_SIGKILL
	case "SIGTERM":
		signal = envdpb.Signal_SIGNAL_SIGTERM
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported signal: %s (use SIGKILL or SIGTERM)", msg.Signal))
	}

	if err := s.mgr.KillProcess(ctx, msg.SandboxId, pid, tag, signal); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("kill process: %w", err))
	}

	return connect.NewResponse(&pb.KillProcessResponse{}), nil
}

func (s *Server) ConnectProcess(
	ctx context.Context,
	req *connect.Request[pb.ConnectProcessRequest],
	stream *connect.ServerStream[pb.ConnectProcessResponse],
) error {
	msg := req.Msg

	var pid uint32
	var tag string
	switch sel := msg.Selector.(type) {
	case *pb.ConnectProcessRequest_Pid:
		pid = sel.Pid
	case *pb.ConnectProcessRequest_Tag:
		tag = sel.Tag
	default:
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("pid or tag is required"))
	}

	events, err := s.mgr.ConnectProcess(ctx, msg.SandboxId, pid, tag)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return connect.NewError(connect.CodeNotFound, err)
		}
		return connect.NewError(connect.CodeInternal, fmt.Errorf("connect process: %w", err))
	}

	for ev := range events {
		var resp pb.ConnectProcessResponse
		switch ev.Type {
		case "start":
			resp.Event = &pb.ConnectProcessResponse_Start{
				Start: &pb.ExecStreamStart{Pid: ev.PID},
			}
		case "stdout":
			resp.Event = &pb.ConnectProcessResponse_Data{
				Data: &pb.ExecStreamData{
					Output: &pb.ExecStreamData_Stdout{Stdout: ev.Data},
				},
			}
		case "stderr":
			resp.Event = &pb.ConnectProcessResponse_Data{
				Data: &pb.ExecStreamData{
					Output: &pb.ExecStreamData_Stderr{Stderr: ev.Data},
				},
			}
		case "end":
			resp.Event = &pb.ConnectProcessResponse_End{
				End: &pb.ExecStreamEnd{
					ExitCode: ev.ExitCode,
					Error:    ev.Error,
				},
			}
		}
		if err := stream.Send(&resp); err != nil {
			return err
		}
	}

	return nil
}
