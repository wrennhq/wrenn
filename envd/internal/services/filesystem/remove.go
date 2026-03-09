// SPDX-License-Identifier: Apache-2.0

package filesystem

import (
	"context"
	"fmt"
	"os"

	"connectrpc.com/connect"

	"git.omukk.dev/wrenn/sandbox/envd/internal/permissions"
	rpc "git.omukk.dev/wrenn/sandbox/envd/internal/services/spec/filesystem"
)

func (s Service) Remove(ctx context.Context, req *connect.Request[rpc.RemoveRequest]) (*connect.Response[rpc.RemoveResponse], error) {
	u, err := permissions.GetAuthUser(ctx, s.defaults.User)
	if err != nil {
		return nil, err
	}

	path, err := permissions.ExpandAndResolve(req.Msg.GetPath(), u, s.defaults.Workdir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	err = os.RemoveAll(path)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("error removing file or directory: %w", err))
	}

	return connect.NewResponse(&rpc.RemoveResponse{}), nil
}
