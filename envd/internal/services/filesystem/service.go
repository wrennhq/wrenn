// SPDX-License-Identifier: Apache-2.0
// Modifications by M/S Omukk

package filesystem

import (
	"connectrpc.com/connect"
	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog"

	"git.omukk.dev/wrenn/sandbox/envd/internal/execcontext"
	"git.omukk.dev/wrenn/sandbox/envd/internal/logs"
	spec "git.omukk.dev/wrenn/sandbox/envd/internal/services/spec/filesystem/filesystemconnect"
	"git.omukk.dev/wrenn/sandbox/envd/internal/utils"
)

type Service struct {
	logger   *zerolog.Logger
	watchers *utils.Map[string, *FileWatcher]
	defaults *execcontext.Defaults
}

func Handle(server *chi.Mux, l *zerolog.Logger, defaults *execcontext.Defaults) {
	service := Service{
		logger:   l,
		watchers: utils.NewMap[string, *FileWatcher](),
		defaults: defaults,
	}

	interceptors := connect.WithInterceptors(
		logs.NewUnaryLogInterceptor(l),
	)

	path, handler := spec.NewFilesystemHandler(service, interceptors)

	server.Mount(path, handler)
}
