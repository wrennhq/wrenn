package envdclient

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	envdpb "git.omukk.dev/wrenn/sandbox/proto/envd/gen"
	"git.omukk.dev/wrenn/sandbox/proto/envd/gen/genconnect"
)

// Client wraps the Connect RPC client for envd's Process and Filesystem services.
type Client struct {
	hostIP     string
	base       string
	healthURL  string
	httpClient *http.Client

	process    genconnect.ProcessClient
	filesystem genconnect.FilesystemClient
}

// New creates a new envd client that connects to the given host IP.
func New(hostIP string) *Client {
	base := baseURL(hostIP)
	httpClient := newHTTPClient()

	return &Client{
		hostIP:     hostIP,
		base:       base,
		healthURL:  base + "/health",
		httpClient: httpClient,
		process:    genconnect.NewProcessClient(httpClient, base),
		filesystem: genconnect.NewFilesystemClient(httpClient, base),
	}
}

// ExecResult holds the output of a command execution.
type ExecResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int32
}

// Exec runs a command inside the sandbox and collects all stdout/stderr output.
// It blocks until the command completes.
func (c *Client) Exec(ctx context.Context, cmd string, args ...string) (*ExecResult, error) {
	stdin := false
	req := connect.NewRequest(&envdpb.StartRequest{
		Process: &envdpb.ProcessConfig{
			Cmd:  cmd,
			Args: args,
		},
		Stdin: &stdin,
	})

	stream, err := c.process.Start(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}
	defer stream.Close()

	result := &ExecResult{}

	for stream.Receive() {
		msg := stream.Msg()
		if msg.Event == nil {
			continue
		}

		event := msg.Event.GetEvent()
		switch e := event.(type) {
		case *envdpb.ProcessEvent_Start:
			slog.Debug("process started", "pid", e.Start.GetPid())

		case *envdpb.ProcessEvent_Data:
			output := e.Data.GetOutput()
			switch o := output.(type) {
			case *envdpb.ProcessEvent_DataEvent_Stdout:
				result.Stdout = append(result.Stdout, o.Stdout...)
			case *envdpb.ProcessEvent_DataEvent_Stderr:
				result.Stderr = append(result.Stderr, o.Stderr...)
			}

		case *envdpb.ProcessEvent_End:
			result.ExitCode = e.End.GetExitCode()
			if e.End.Error != nil {
				slog.Debug("process ended with error",
					"exit_code", e.End.GetExitCode(),
					"error", e.End.GetError(),
				)
			}

		case *envdpb.ProcessEvent_Keepalive:
			// Ignore keepalives.
		}
	}

	if err := stream.Err(); err != nil && err != io.EOF {
		return result, fmt.Errorf("stream error: %w", err)
	}

	return result, nil
}

// WriteFile writes content to a file inside the sandbox via envd's filesystem service.
func (c *Client) WriteFile(ctx context.Context, path string, content []byte) error {
	// envd uses HTTP upload for files, not Connect RPC.
	// POST /files with multipart form data.
	// For now, use the filesystem MakeDir for directories.
	// TODO: Implement file upload via envd's REST endpoint.
	return fmt.Errorf("WriteFile not yet implemented")
}

// ReadFile reads a file from inside the sandbox.
func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, error) {
	// TODO: Implement file download via envd's REST endpoint.
	return nil, fmt.Errorf("ReadFile not yet implemented")
}

// ListDir lists directory contents inside the sandbox.
func (c *Client) ListDir(ctx context.Context, path string, depth uint32) (*envdpb.ListDirResponse, error) {
	req := connect.NewRequest(&envdpb.ListDirRequest{
		Path:  path,
		Depth: depth,
	})

	resp, err := c.filesystem.ListDir(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("list dir: %w", err)
	}

	return resp.Msg, nil
}
