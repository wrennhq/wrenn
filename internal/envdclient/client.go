package envdclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"

	"connectrpc.com/connect"

	envdpb "git.omukk.dev/wrenn/wrenn/proto/envd/gen"
	"git.omukk.dev/wrenn/wrenn/proto/envd/gen/genconnect"
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

// BaseURL returns the HTTP base URL for reaching envd.
func (c *Client) BaseURL() string {
	return c.base
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

// ExecStreamEvent represents a single event from a streaming exec.
type ExecStreamEvent struct {
	Type     string // "start", "stdout", "stderr", "end"
	PID      uint32
	Data     []byte
	ExitCode int32
	Error    string
}

// ExecStream runs a command inside the sandbox and returns a channel of output events.
// The channel is closed when the process ends or the context is cancelled.
func (c *Client) ExecStream(ctx context.Context, cmd string, args ...string) (<-chan ExecStreamEvent, error) {
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

	ch := make(chan ExecStreamEvent, 16)
	go func() {
		defer close(ch)
		defer stream.Close()

		for stream.Receive() {
			msg := stream.Msg()
			if msg.Event == nil {
				continue
			}

			var ev ExecStreamEvent
			event := msg.Event.GetEvent()
			switch e := event.(type) {
			case *envdpb.ProcessEvent_Start:
				ev = ExecStreamEvent{Type: "start", PID: e.Start.GetPid()}

			case *envdpb.ProcessEvent_Data:
				output := e.Data.GetOutput()
				switch o := output.(type) {
				case *envdpb.ProcessEvent_DataEvent_Stdout:
					ev = ExecStreamEvent{Type: "stdout", Data: o.Stdout}
				case *envdpb.ProcessEvent_DataEvent_Stderr:
					ev = ExecStreamEvent{Type: "stderr", Data: o.Stderr}
				}

			case *envdpb.ProcessEvent_End:
				ev = ExecStreamEvent{Type: "end", ExitCode: e.End.GetExitCode()}
				if e.End.Error != nil {
					ev.Error = e.End.GetError()
				}

			case *envdpb.ProcessEvent_Keepalive:
				continue
			}

			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}

		if err := stream.Err(); err != nil && err != io.EOF {
			slog.Debug("exec stream error", "error", err)
		}
	}()

	return ch, nil
}

// WriteFile writes content to a file inside the sandbox via envd's REST endpoint.
// envd expects POST /files?path=...&username=root with multipart/form-data (field name "file").
func (c *Client) WriteFile(ctx context.Context, path string, content []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", "upload")
	if err != nil {
		return fmt.Errorf("create multipart: %w", err)
	}
	if _, err := part.Write(content); err != nil {
		return fmt.Errorf("write multipart: %w", err)
	}
	writer.Close()

	u := fmt.Sprintf("%s/files?%s", c.base, url.Values{
		"path":     {path},
		"username": {"root"},
	}.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("write file %s: status %d: %s", path, resp.StatusCode, string(respBody))
	}

	slog.Debug("envd write file", "path", path, "status", resp.StatusCode, "response", string(respBody))
	return nil
}

// ReadFile reads a file from inside the sandbox via envd's REST endpoint.
// envd expects GET /files?path=...&username=root.
func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, error) {
	u := fmt.Sprintf("%s/files?%s", c.base, url.Values{
		"path":     {path},
		"username": {"root"},
	}.Encode())

	slog.Debug("envd read file", "url", u, "path", path)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("read file %s: status %d: %s", path, resp.StatusCode, string(respBody))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read file body: %w", err)
	}

	return data, nil
}

// PostInit calls envd's POST /init endpoint, which triggers a re-read of
// Firecracker MMDS metadata. This updates WRENN_SANDBOX_ID, WRENN_TEMPLATE_ID
// env vars and the corresponding files under /run/wrenn/ inside the guest.
// Must be called after snapshot restore so envd picks up the new sandbox's metadata.
func (c *Client) PostInit(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/init", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post init: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post init: status %d: %s", resp.StatusCode, string(body))
	}

	return nil
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

// MakeDir creates a directory inside the sandbox.
func (c *Client) MakeDir(ctx context.Context, path string) (*envdpb.MakeDirResponse, error) {
	req := connect.NewRequest(&envdpb.MakeDirRequest{
		Path: path,
	})

	resp, err := c.filesystem.MakeDir(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("make dir: %w", err)
	}

	return resp.Msg, nil
}

// Remove removes a file or directory inside the sandbox.
func (c *Client) Remove(ctx context.Context, path string) error {
	req := connect.NewRequest(&envdpb.RemoveRequest{
		Path: path,
	})

	if _, err := c.filesystem.Remove(ctx, req); err != nil {
		return fmt.Errorf("remove: %w", err)
	}

	return nil
}
