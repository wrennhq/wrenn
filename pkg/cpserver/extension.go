package cpserver

import "git.omukk.dev/wrenn/wrenn/pkg/cpextension"

// ServerContext is an alias for cpextension.ServerContext.
// Enterprise code should use this package (pkg/cpserver) as the main entry point.
type ServerContext = cpextension.ServerContext

// Extension is an alias for cpextension.Extension.
// Enterprise code should use this package (pkg/cpserver) as the main entry point.
type Extension = cpextension.Extension
