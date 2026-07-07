package envdclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"connectrpc.com/connect"

	"github.com/google/uuid"

	envdpb "git.omukk.dev/wrenn/wrenn/proto/envd/gen"
	"git.omukk.dev/wrenn/wrenn/proto/envd/gen/genconnect"
)

// Client wraps the Connect RPC client for envd's Process and Filesystem services.
type Client struct {
	hostIP          string
	base            string
	healthURL       string
	activityURL     string
	httpClient      *http.Client
	streamingClient *http.Client

	process    genconnect.ProcessClient
	filesystem genconnect.FilesystemClient
}

// New creates a new envd client that connects to the given host IP.
func New(hostIP string) *Client {
	return newWithBase(hostIP, baseURL(hostIP))
}

// NewWithBaseURL creates an envd client against an explicit base URL instead
// of the standard http://{hostIP}:{envdPort}. Intended for tests (httptest
// servers) and nonstandard listeners.
func NewWithBaseURL(base string) *Client {
	u, err := url.Parse(base)
	host := base
	if err == nil {
		host = u.Hostname()
	}
	return newWithBase(host, base)
}

func newWithBase(hostIP, base string) *Client {
	httpClient := newHTTPClient()
	streamingClient := newStreamingHTTPClient()

	return &Client{
		hostIP:          hostIP,
		base:            base,
		healthURL:       base + "/health",
		activityURL:     base + "/activity",
		httpClient:      httpClient,
		streamingClient: streamingClient,
		process:         genconnect.NewProcessClient(streamingClient, base),
		filesystem:      genconnect.NewFilesystemClient(httpClient, base),
	}
}

// CloseIdleConnections closes idle connections on both the unary and streaming
// transports. Call this before taking a VM snapshot to remove stale TCP state
// from the guest.
func (c *Client) CloseIdleConnections() {
	c.httpClient.CloseIdleConnections()
	c.streamingClient.CloseIdleConnections()
}

// BaseURL returns the HTTP base URL for reaching envd.
func (c *Client) BaseURL() string {
	return c.base
}

// HTTPClient returns the http.Client with a 2-minute request timeout.
// Suitable for short-lived envd calls (health, init, snapshot/prepare).
func (c *Client) HTTPClient() *http.Client {
	return c.httpClient
}

// StreamingHTTPClient returns the http.Client without a request timeout.
// Use for streaming file transfers or any request that may run indefinitely.
func (c *Client) StreamingHTTPClient() *http.Client {
	return c.streamingClient
}

// ExecResult holds the output of a command execution.
type ExecResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int32
}

// ExecOpts holds optional parameters for Exec.
type ExecOpts struct {
	Envs map[string]string
	Cwd  string
}

// Exec runs a command inside the sandbox and collects all stdout/stderr output.
// It blocks until the command completes.
func (c *Client) Exec(ctx context.Context, cmd string, args []string, opts *ExecOpts) (*ExecResult, error) {
	stdin := false
	proc := &envdpb.ProcessConfig{
		Cmd:  cmd,
		Args: args,
	}
	if opts != nil {
		if len(opts.Envs) > 0 {
			proc.Envs = opts.Envs
		}
		if opts.Cwd != "" {
			proc.Cwd = &opts.Cwd
		}
	}
	req := connect.NewRequest(&envdpb.StartRequest{
		Process: proc,
		Stdin:   &stdin,
	})

	stream, err := c.process.Start(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}
	defer stream.Close()

	result := &ExecResult{}

	for stream.Receive() {
		ev, ok := procEventToStreamEvent(stream.Msg().GetEvent())
		if !ok {
			continue
		}
		switch ev.Type {
		case "stdout":
			result.Stdout = append(result.Stdout, ev.Data...)
		case "stderr":
			result.Stderr = append(result.Stderr, ev.Data...)
		case "end":
			result.ExitCode = ev.ExitCode
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

// procEventToStreamEvent converts a raw envd ProcessEvent into an
// ExecStreamEvent. The second return is false for events with no payload to
// forward (nil event, keepalive, unknown data variant) so callers can skip
// them. This is the single decoder shared by Exec, ExecStream and
// ConnectProcess.
func procEventToStreamEvent(pe *envdpb.ProcessEvent) (ExecStreamEvent, bool) {
	if pe == nil {
		return ExecStreamEvent{}, false
	}
	switch e := pe.GetEvent().(type) {
	case *envdpb.ProcessEvent_Start:
		return ExecStreamEvent{Type: "start", PID: e.Start.GetPid()}, true
	case *envdpb.ProcessEvent_Data:
		switch o := e.Data.GetOutput().(type) {
		case *envdpb.ProcessEvent_DataEvent_Stdout:
			return ExecStreamEvent{Type: "stdout", Data: o.Stdout}, true
		case *envdpb.ProcessEvent_DataEvent_Stderr:
			return ExecStreamEvent{Type: "stderr", Data: o.Stderr}, true
		}
		return ExecStreamEvent{}, false
	case *envdpb.ProcessEvent_End:
		ev := ExecStreamEvent{Type: "end", ExitCode: e.End.GetExitCode()}
		if e.End.Error != nil {
			ev.Error = e.End.GetError()
		}
		return ev, true
	}
	return ExecStreamEvent{}, false
}

// procEventStream is the subset of a Connect server-stream that pumpProcessEvents
// needs. Both *connect.ServerStreamForClient[StartResponse] and
// [ConnectResponse] satisfy it.
type procEventStream[T any] interface {
	Receive() bool
	Msg() *T
	Err() error
	Close() error
}

// pumpProcessEvents drains a process server-stream into ch until the stream ends
// or ctx is cancelled, closing ch on exit. getEvent extracts the ProcessEvent
// from each message so the same loop works for both the Start and Connect RPCs.
func pumpProcessEvents[T any](
	ctx context.Context,
	stream procEventStream[T],
	getEvent func(*T) *envdpb.ProcessEvent,
	ch chan<- ExecStreamEvent,
	logLabel string,
) {
	defer close(ch)
	defer stream.Close()

	for stream.Receive() {
		ev, ok := procEventToStreamEvent(getEvent(stream.Msg()))
		if !ok {
			continue
		}
		select {
		case ch <- ev:
		case <-ctx.Done():
			return
		}
	}

	if err := stream.Err(); err != nil && err != io.EOF {
		slog.Debug(logLabel, "error", err)
	}
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

	ch := make(chan ExecStreamEvent, 256)
	go pumpProcessEvents(ctx, stream, (*envdpb.StartResponse).GetEvent, ch, "exec stream error")

	return ch, nil
}

// WriteFile writes content to a file inside the sandbox via envd's REST endpoint.
// envd expects PUT /files?path=...&username=root with the raw file content as the body.
func (c *Client) WriteFile(ctx context.Context, path string, content []byte) error {
	u := fmt.Sprintf("%s/files?%s", c.base, url.Values{
		"path":     {path},
		"username": {"root"},
	}.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(content))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
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

// PrepareSnapshot calls envd's POST /snapshot/prepare endpoint, which stops
// the port scanner/forwarder and marks active connections for post-restore
// cleanup before the VMM freezes vCPUs.
//
// Best-effort: the caller should log a warning on error but not abort the pause.
func (c *Client) PrepareSnapshot(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/snapshot/prepare", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("prepare snapshot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("prepare snapshot: status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// MemoryPreloadStatus mirrors envd's /memory/preload response.
//
// State values: "idle", "running", "done", "failed", "cancelled".
type MemoryPreloadStatus struct {
	State      string  `json:"state"`
	Regions    uint64  `json:"regions"`
	Pages      uint64  `json:"pages"`
	Bytes      uint64  `json:"bytes"`
	ElapsedSec float64 `json:"elapsed_sec"`
	Source     string  `json:"source"`
	Error      string  `json:"error,omitempty"`
}

// StartMemoryPreload posts to envd's /memory/preload to spawn a guest-side
// loader that reads every physical RAM page. The request returns immediately
// after the loader is queued — the actual materialisation runs in a detached
// thread inside envd. Required after a snapshot restore with
// memory_restore_mode=ondemand so the next ch.snapshot writes a
// self-contained memory-ranges file.
//
// Use WaitMemoryPreload to block on completion or GetMemoryPreloadStatus to
// query progress.
func (c *Client) StartMemoryPreload(ctx context.Context) (MemoryPreloadStatus, error) {
	return c.memoryPreloadRequest(ctx, http.MethodPost)
}

// GetMemoryPreloadStatus reads envd's /memory/preload status without
// starting a new loader.
func (c *Client) GetMemoryPreloadStatus(ctx context.Context) (MemoryPreloadStatus, error) {
	return c.memoryPreloadRequest(ctx, http.MethodGet)
}

func (c *Client) memoryPreloadRequest(ctx context.Context, method string) (MemoryPreloadStatus, error) {
	var status MemoryPreloadStatus
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/memory/preload", nil)
	if err != nil {
		return status, fmt.Errorf("create request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return status, fmt.Errorf("memory preload %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return status, fmt.Errorf("memory preload %s: status %d: %s", method, resp.StatusCode, string(body))
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return status, fmt.Errorf("memory preload %s: decode: %w", method, err)
	}
	return status, nil
}

// WaitMemoryPreload polls envd until the loader is no longer running or ctx
// is cancelled. Returns the final status. Polling interval is fixed at 1s —
// the loader runs for many seconds to minutes, so finer polling wastes RPCs.
func (c *Client) WaitMemoryPreload(ctx context.Context) (MemoryPreloadStatus, error) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		status, err := c.GetMemoryPreloadStatus(ctx)
		if err != nil {
			return status, err
		}
		if status.State != "running" {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-ticker.C:
		}
	}
}

// CancelMemoryPreload signals the in-guest memory preloader to stop early.
// Used during teardown so a pause/destroy doesn't have to wait for a
// multi-hundred-MiB read to finish.
func (c *Client) CancelMemoryPreload(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/memory/preload/cancel", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("preload cancel: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("preload cancel: status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// PostInit calls envd's POST /init endpoint to trigger post-boot or
// post-restore initialization. sandbox_id and template_id are passed
// so envd can set WRENN_SANDBOX_ID and WRENN_TEMPLATE_ID env vars.
func (c *Client) PostInit(ctx context.Context) error {
	return c.PostInitWithDefaults(ctx, "", nil, "", "", "")
}

// PostInitWithDefaults calls envd's POST /init endpoint with optional default
// user, environment variables, and sandbox metadata. These are applied to
// envd's defaults so all subsequent process executions use them.
//
// timestamp and lifecycle_id are always populated: envd uses them to snap
// the guest clock to the host's wall time and to detect post-resume calls
// (which trigger port-forwarder restart + NFS remount).
func (c *Client) PostInitWithDefaults(ctx context.Context, defaultUser string, envVars map[string]string, sandboxID, templateID, proxyDomain string) error {
	payload := map[string]any{
		"timestamp":    time.Now().UTC().Format(time.RFC3339Nano),
		"lifecycle_id": uuid.NewString(),
	}
	if defaultUser != "" {
		payload["defaultUser"] = defaultUser
	}
	if len(envVars) > 0 {
		payload["envVars"] = envVars
	}
	if sandboxID != "" {
		payload["sandbox_id"] = sandboxID
	}
	if templateID != "" {
		payload["template_id"] = templateID
	}
	if proxyDomain != "" {
		payload["proxy_domain"] = proxyDomain
	}

	var body io.Reader
	if len(payload) > 0 {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal init body: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/init", body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("post init: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("post init: status %d: %s", resp.StatusCode, string(respBody))
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
