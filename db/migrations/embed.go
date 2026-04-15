// Package migrations embeds the SQL migration files so that external modules
// (such as the enterprise edition) can access them programmatically.
package migrations

import "embed"

// FS contains all SQL migration files.
//
//go:embed *.sql
var FS embed.FS
