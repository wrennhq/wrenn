package envdclient

import (
	"context"
	"fmt"
	"io"

	"connectrpc.com/connect"

	envdpb "git.omukk.dev/wrenn/wrenn/proto/envd/gen"
)

// ProcessInfo holds metadata about a running process inside the sandbox.
type ProcessInfo struct {
	PID  uint32
	Tag  string
	Cmd  string
	Args []string
}

// StartBackground starts a process that runs independently of the RPC stream.
// It opens a Start stream, reads the first StartEvent to obtain the PID,
// then closes the stream. The process continues running inside the VM because
// envd binds it to context.Background().
func (c *Client) StartBackground(ctx context.Context, tag, cmd string, args []string, envs map[string]string, cwd string) (uint32, error) {
	stdin := false
	cfg := &envdpb.ProcessConfig{
		Cmd:  cmd,
		Args: args,
		Envs: envs,
	}
	if cwd != "" {
		cfg.Cwd = &cwd
	}

	req := connect.NewRequest(&envdpb.StartRequest{
		Process: cfg,
		Tag:     &tag,
		Stdin:   &stdin,
	})

	stream, err := c.process.Start(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("start background process: %w", err)
	}
	defer stream.Close()

	// Read events until we get the StartEvent with the PID.
	for stream.Receive() {
		msg := stream.Msg()
		if msg.Event == nil {
			continue
		}
		if start, ok := msg.Event.GetEvent().(*envdpb.ProcessEvent_Start); ok {
			return start.Start.GetPid(), nil
		}
	}

	if err := stream.Err(); err != nil && err != io.EOF {
		return 0, fmt.Errorf("start background process stream: %w", err)
	}

	return 0, fmt.Errorf("start background process: no start event received")
}

// ConnectProcess re-attaches to a running process by PID or tag and returns
// a channel of streaming events. The channel is closed when the process ends
// or the context is cancelled.
func (c *Client) ConnectProcess(ctx context.Context, pid uint32, tag string) (<-chan ExecStreamEvent, error) {
	var selector *envdpb.ProcessSelector
	if tag != "" {
		selector = &envdpb.ProcessSelector{
			Selector: &envdpb.ProcessSelector_Tag{Tag: tag},
		}
	} else {
		selector = &envdpb.ProcessSelector{
			Selector: &envdpb.ProcessSelector_Pid{Pid: pid},
		}
	}

	stream, err := c.process.Connect(ctx, connect.NewRequest(&envdpb.ConnectRequest{
		Process: selector,
	}))
	if err != nil {
		return nil, fmt.Errorf("connect process: %w", err)
	}

	ch := make(chan ExecStreamEvent, 16)
	go pumpProcessEvents(ctx, stream, (*envdpb.ConnectResponse).GetEvent, ch, "connect process stream error")

	return ch, nil
}

// ListProcesses returns all running processes inside the sandbox.
func (c *Client) ListProcesses(ctx context.Context) ([]ProcessInfo, error) {
	resp, err := c.process.List(ctx, connect.NewRequest(&envdpb.ListRequest{}))
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}

	procs := make([]ProcessInfo, 0, len(resp.Msg.Processes))
	for _, p := range resp.Msg.Processes {
		info := ProcessInfo{
			PID: p.Pid,
		}
		if p.Tag != nil {
			info.Tag = *p.Tag
		}
		if p.Config != nil {
			info.Cmd = p.Config.Cmd
			info.Args = p.Config.Args
		}
		procs = append(procs, info)
	}

	return procs, nil
}

// KillProcess sends a signal to a process identified by PID or tag.
func (c *Client) KillProcess(ctx context.Context, pid uint32, tag string, signal envdpb.Signal) error {
	var selector *envdpb.ProcessSelector
	if tag != "" {
		selector = &envdpb.ProcessSelector{
			Selector: &envdpb.ProcessSelector_Tag{Tag: tag},
		}
	} else {
		selector = &envdpb.ProcessSelector{
			Selector: &envdpb.ProcessSelector_Pid{Pid: pid},
		}
	}

	_, err := c.process.SendSignal(ctx, connect.NewRequest(&envdpb.SendSignalRequest{
		Process: selector,
		Signal:  signal,
	}))
	if err != nil {
		return fmt.Errorf("kill process: %w", err)
	}

	return nil
}
