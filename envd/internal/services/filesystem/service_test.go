package filesystem

import (
	"git.omukk.dev/wrenn/sandbox/envd/internal/execcontext"
	"git.omukk.dev/wrenn/sandbox/envd/internal/utils"
)

func mockService() Service {
	return Service{
		defaults: &execcontext.Defaults{
			EnvVars: utils.NewMap[string, string](),
		},
	}
}
