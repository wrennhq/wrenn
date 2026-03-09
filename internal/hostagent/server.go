package hostagent

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	pb "git.omukk.dev/wrenn/sandbox/proto/hostagent/gen"
	"git.omukk.dev/wrenn/sandbox/proto/hostagent/gen/hostagentv1connect"

	"git.omukk.dev/wrenn/sandbox/internal/sandbox"
)

// Server implements the HostAgentService Connect RPC handler.
type Server struct {
	hostagentv1connect.UnimplementedHostAgentServiceHandler
	mgr *sandbox.Manager
}

// NewServer creates a new host agent RPC server.
func NewServer(mgr *sandbox.Manager) *Server {
	return &Server{mgr: mgr}
}

func (s *Server) CreateSandbox(
	ctx context.Context,
	req *connect.Request[pb.CreateSandboxRequest],
) (*connect.Response[pb.CreateSandboxResponse], error) {
	msg := req.Msg

	sb, err := s.mgr.Create(ctx, msg.Template, int(msg.Vcpus), int(msg.MemoryMb), int(msg.TimeoutSec))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create sandbox: %w", err))
	}

	return connect.NewResponse(&pb.CreateSandboxResponse{
		SandboxId: sb.ID,
		Status:    string(sb.Status),
		HostIp:    sb.HostIP.String(),
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
	if err := s.mgr.Resume(ctx, req.Msg.SandboxId); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&pb.ResumeSandboxResponse{}), nil
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

func (s *Server) ListSandboxes(
	ctx context.Context,
	req *connect.Request[pb.ListSandboxesRequest],
) (*connect.Response[pb.ListSandboxesResponse], error) {
	sandboxes := s.mgr.List()

	infos := make([]*pb.SandboxInfo, len(sandboxes))
	for i, sb := range sandboxes {
		infos[i] = &pb.SandboxInfo{
			SandboxId:       sb.ID,
			Status:          string(sb.Status),
			Template:        sb.Template,
			Vcpus:           int32(sb.VCPUs),
			MemoryMb:        int32(sb.MemoryMB),
			HostIp:          sb.HostIP.String(),
			CreatedAtUnix:   sb.CreatedAt.Unix(),
			LastActiveAtUnix: sb.LastActiveAt.Unix(),
			TimeoutSec:      int32(sb.TimeoutSec),
		}
	}

	return connect.NewResponse(&pb.ListSandboxesResponse{
		Sandboxes: infos,
	}), nil
}
