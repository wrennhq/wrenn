package main

import "git.omukk.dev/wrenn/wrenn/pkg/cpserver"

// Set via -ldflags at build time.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	cpserver.Run(
		cpserver.WithVersion(version, commit),
	)
}
