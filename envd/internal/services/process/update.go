// SPDX-License-Identifier: Apache-2.0

package process

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/creack/pty"

	rpc "git.omukk.dev/wrenn/sandbox/envd/internal/services/spec/process"
)

func (s *Service) Update(_ context.Context, req *connect.Request[rpc.UpdateRequest]) (*connect.Response[rpc.UpdateResponse], error) {
	proc, err := s.getProcess(req.Msg.GetProcess())
	if err != nil {
		return nil, err
	}

	if req.Msg.GetPty() != nil {
		err := proc.ResizeTty(&pty.Winsize{
			Rows: uint16(req.Msg.GetPty().GetSize().GetRows()),
			Cols: uint16(req.Msg.GetPty().GetSize().GetCols()),
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("error resizing tty: %w", err))
		}
	}

	return connect.NewResponse(&rpc.UpdateResponse{}), nil
}
