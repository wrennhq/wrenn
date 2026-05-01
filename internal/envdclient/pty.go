package envdclient

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"connectrpc.com/connect"

	envdpb "git.omukk.dev/wrenn/wrenn/proto/envd/gen"
)

// PtyEvent represents a single event from a PTY output stream.
type PtyEvent struct {
	Type     string // "started", "output", "end"
	PID      uint32
	Data     []byte
	ExitCode int32
	Error    string
}

// PtyStart starts a new PTY process in the guest and returns a channel of events.
// The tag is the stable identifier used to reconnect via PtyConnect.
// The channel is closed when the process ends or ctx is cancelled.
// NOTE: The user parameter from PtyAttachRequest is not yet supported by envd's
// ProcessConfig proto. When envd adds user support, thread it through here.
func (c *Client) PtyStart(ctx context.Context, tag, cmd string, args []string, cols, rows uint32, envs map[string]string, cwd string) (<-chan PtyEvent, error) {
	stdin := true
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
		Pty: &envdpb.PTY{
			Size: &envdpb.PTY_Size{
				Cols: cols,
				Rows: rows,
			},
		},
		Tag:   &tag,
		Stdin: &stdin,
	})

	stream, err := c.process.Start(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}

	return drainPtyStream(ctx, &startStream{s: stream}, true), nil
}

// PtyConnect re-attaches to an existing PTY process by tag.
// Returns a channel of output events starting from the current point.
func (c *Client) PtyConnect(ctx context.Context, tag string) (<-chan PtyEvent, error) {
	req := connect.NewRequest(&envdpb.ConnectRequest{
		Process: &envdpb.ProcessSelector{
			Selector: &envdpb.ProcessSelector_Tag{Tag: tag},
		},
	})

	stream, err := c.process.Connect(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("pty connect: %w", err)
	}

	return drainPtyStream(ctx, &connectStream{s: stream}, false), nil
}

// PtySendInput sends raw bytes to the PTY process identified by tag.
func (c *Client) PtySendInput(ctx context.Context, tag string, data []byte) error {
	req := connect.NewRequest(&envdpb.SendInputRequest{
		Process: &envdpb.ProcessSelector{
			Selector: &envdpb.ProcessSelector_Tag{Tag: tag},
		},
		Input: &envdpb.ProcessInput{
			Input: &envdpb.ProcessInput_Pty{Pty: data},
		},
	})

	if _, err := c.process.SendInput(ctx, req); err != nil {
		return fmt.Errorf("pty send input: %w", err)
	}
	return nil
}

// PtyResize updates the terminal dimensions for the PTY process identified by tag.
func (c *Client) PtyResize(ctx context.Context, tag string, cols, rows uint32) error {
	req := connect.NewRequest(&envdpb.UpdateRequest{
		Process: &envdpb.ProcessSelector{
			Selector: &envdpb.ProcessSelector_Tag{Tag: tag},
		},
		Pty: &envdpb.PTY{
			Size: &envdpb.PTY_Size{
				Cols: cols,
				Rows: rows,
			},
		},
	})

	if _, err := c.process.Update(ctx, req); err != nil {
		return fmt.Errorf("pty resize: %w", err)
	}
	return nil
}

// PtyKill sends SIGKILL to the PTY process identified by tag.
func (c *Client) PtyKill(ctx context.Context, tag string) error {
	req := connect.NewRequest(&envdpb.SendSignalRequest{
		Process: &envdpb.ProcessSelector{
			Selector: &envdpb.ProcessSelector_Tag{Tag: tag},
		},
		Signal: envdpb.Signal_SIGNAL_SIGKILL,
	})

	if _, err := c.process.SendSignal(ctx, req); err != nil {
		return fmt.Errorf("pty kill: %w", err)
	}
	return nil
}

// eventStream is an interface covering both StartResponse and ConnectResponse streams.
type eventStream interface {
	Receive() bool
	Err() error
	Close() error
}

type startStream struct {
	s *connect.ServerStreamForClient[envdpb.StartResponse]
}

func (s *startStream) Receive() bool { return s.s.Receive() }
func (s *startStream) Err() error    { return s.s.Err() }
func (s *startStream) Close() error  { return s.s.Close() }
func (s *startStream) Event() *envdpb.ProcessEvent {
	return s.s.Msg().GetEvent()
}

type connectStream struct {
	s *connect.ServerStreamForClient[envdpb.ConnectResponse]
}

func (s *connectStream) Receive() bool { return s.s.Receive() }
func (s *connectStream) Err() error    { return s.s.Err() }
func (s *connectStream) Close() error  { return s.s.Close() }
func (s *connectStream) Event() *envdpb.ProcessEvent {
	return s.s.Msg().GetEvent()
}

type eventProvider interface {
	eventStream
	Event() *envdpb.ProcessEvent
}

// drainPtyStream reads events from either a Start or Connect stream and maps
// them into PtyEvent values on a channel.
func drainPtyStream(ctx context.Context, stream eventProvider, expectStart bool) <-chan PtyEvent {
	ch := make(chan PtyEvent, 256)
	go func() {
		defer close(ch)
		defer stream.Close()

		for stream.Receive() {
			event := stream.Event()
			if event == nil {
				continue
			}

			var ev PtyEvent
			switch e := event.GetEvent().(type) {
			case *envdpb.ProcessEvent_Start:
				if expectStart {
					ev = PtyEvent{Type: "started", PID: e.Start.GetPid()}
				} else {
					continue
				}

			case *envdpb.ProcessEvent_Data:
				switch o := e.Data.GetOutput().(type) {
				case *envdpb.ProcessEvent_DataEvent_Pty:
					ev = PtyEvent{Type: "output", Data: o.Pty}
				case *envdpb.ProcessEvent_DataEvent_Stdout:
					ev = PtyEvent{Type: "output", Data: o.Stdout}
				case *envdpb.ProcessEvent_DataEvent_Stderr:
					ev = PtyEvent{Type: "output", Data: o.Stderr}
				default:
					continue
				}

			case *envdpb.ProcessEvent_End:
				ev = PtyEvent{Type: "end", ExitCode: e.End.GetExitCode()}
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
			slog.Debug("pty stream error", "error", err)
		}
	}()

	return ch
}
