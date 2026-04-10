// SPDX-License-Identifier: Apache-2.0

package process

import (
	"fmt"

	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"git.omukk.dev/wrenn/sandbox/envd/internal/execcontext"
	"git.omukk.dev/wrenn/sandbox/envd/internal/logs"
	"git.omukk.dev/wrenn/sandbox/envd/internal/services/cgroups"
	"git.omukk.dev/wrenn/sandbox/envd/internal/services/process/handler"
	rpc "git.omukk.dev/wrenn/sandbox/envd/internal/services/spec/process"
	spec "git.omukk.dev/wrenn/sandbox/envd/internal/services/spec/process/processconnect"
	"git.omukk.dev/wrenn/sandbox/envd/internal/utils"
)

type Service struct {
	processes     *utils.Map[uint32, *handler.Handler]
	logger        *zerolog.Logger
	defaults      *execcontext.Defaults
	cgroupManager cgroups.Manager
}

func newService(l *zerolog.Logger, defaults *execcontext.Defaults, cgroupManager cgroups.Manager) *Service {
	return &Service{
		logger:        l,
		processes:     utils.NewMap[uint32, *handler.Handler](),
		defaults:      defaults,
		cgroupManager: cgroupManager,
	}
}

func Handle(server *chi.Mux, l *zerolog.Logger, defaults *execcontext.Defaults, cgroupManager cgroups.Manager) *Service {
	service := newService(l, defaults, cgroupManager)

	interceptors := connect.WithInterceptors(logs.NewUnaryLogInterceptor(l))

	path, h := spec.NewProcessHandler(service, interceptors)

	server.Mount(path, h)

	return service
}

func (s *Service) getProcess(selector *rpc.ProcessSelector) (*handler.Handler, error) {
	var proc *handler.Handler

	switch selector.GetSelector().(type) {
	case *rpc.ProcessSelector_Pid:
		p, ok := s.processes.Load(selector.GetPid())
		if !ok {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("process with pid %d not found", selector.GetPid()))
		}

		proc = p
	case *rpc.ProcessSelector_Tag:
		tag := selector.GetTag()

		s.processes.Range(func(_ uint32, value *handler.Handler) bool {
			if value.Tag == nil {
				return true // no tag, keep looking
			}

			if *value.Tag == tag {
				proc = value
				return false // found, stop iterating
			}

			return true // different tag, keep looking
		})

		if proc == nil {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("process with tag %s not found", tag))
		}

	default:
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("invalid input type %T", selector))
	}

	return proc, nil
}
