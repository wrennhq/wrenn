package recipe

import (
	"regexp"
	"slices"
	"strings"
)

// ExecContext holds mutable state that persists across recipe steps.
// It is initialized empty and updated by ENV, WORKDIR, and USER steps.
type ExecContext struct {
	WorkDir string
	EnvVars map[string]string
	User    string // Current unix user for command execution.
}

// This regex matches:
// 1. $$ (escaped dollar)
// 2. ${VAR} or ${} (braced variable, possibly empty)
// 3. $VAR (bare variable)
var envRegex = regexp.MustCompile(`\$\$|\$\{([a-zA-Z0-9_]*)\}|\$([a-zA-Z0-9_]+)`)

// WrappedCommand returns the full shell command for a RUN step with context
// applied. The result is handed to the exec layer as a bare command (no
// "-c" wrapper), so the user's default login shell — resolved by envd from
// /etc/passwd inside the VM — interprets it.
//
// If WORKDIR and/or ENV are set, they are prepended as a shell preamble:
//
//	cd '/the/dir' && export KEY='val' && original command
//
// If USER is set to a non-root user, the entire command is wrapped with su.
// Dropping `-s` lets su run it under that user's login shell rather than
// forcing /bin/sh:
//
//	su <user> -c '<preamble + command>'
func (c *ExecContext) WrappedCommand(cmd string) string {
	inner := c.innerCommand(cmd)
	if c.User != "" && c.User != "root" {
		return "su " + shellescape(c.User) + " -c " + shellescape(inner)
	}
	return inner
}

// innerCommand applies the workdir/env preamble to cmd without user wrapping.
// The preamble cds into WORKDIR and exports ENV vars, then the command runs in
// the same (login) shell — no nested shell is named, so the user's default
// shell interprets the command and any pipes/operators within it.
func (c *ExecContext) innerCommand(cmd string) string {
	return c.shellPrefix() + cmd
}

// StartCommand returns the shell command for a START step. The process is
// launched in the background via nohup so that the outer shell exits
// immediately, allowing the build to continue. stdout/stderr of the
// background process are discarded (the process keeps running in the VM).
//
// Multiple START steps can be issued to run several background processes
// simultaneously before a healthcheck is evaluated.
func (c *ExecContext) StartCommand(cmd string) string {
	// Launch the background process under the user's login shell. $SHELL is set
	// to that shell by su (non-root) or by envd (root), with /bin/sh as a safe
	// fallback if it is somehow unset.
	prefix := c.shellPrefix()
	inner := prefix + `nohup "${SHELL:-/bin/sh}" -c ` + shellescape(cmd) + " >/dev/null 2>&1 &"
	if c.User != "" && c.User != "root" {
		return "su " + shellescape(c.User) + " -c " + shellescape(inner)
	}
	return inner
}

// shellPrefix builds the "cd '/dir' && export KEY='val' && " preamble for a
// shell command. ENV vars are exported (not just assignment-prefixed) so they
// apply to the whole command — including any pipes or && chains — and to child
// processes, matching Dockerfile ENV semantics. Returns an empty string when no
// context is set.
func (c *ExecContext) shellPrefix() string {
	if c.WorkDir == "" && len(c.EnvVars) == 0 {
		return ""
	}
	var sb strings.Builder
	if c.WorkDir != "" {
		sb.WriteString("cd ")
		sb.WriteString(shellescape(c.WorkDir))
		sb.WriteString(" && ")
	}
	if len(c.EnvVars) > 0 {
		keys := make([]string, 0, len(c.EnvVars))
		for k := range c.EnvVars {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		sb.WriteString("export")
		for _, k := range keys {
			sb.WriteByte(' ')
			sb.WriteString(k)
			sb.WriteByte('=')
			sb.WriteString(shellescape(c.EnvVars[k]))
		}
		sb.WriteString(" && ")
	}
	return sb.String()
}

// expandEnv replaces $var and ${var} placeholders in the string s with their
// corresponding values from the vars map.
// It supports escaping with $$, which is replaced by a single $.
// If a variable is not found in the vars map, it is replaced with an empty
// string.
func expandEnv(s string, vars map[string]string) string {
	return envRegex.ReplaceAllStringFunc(s, func(match string) string {
		if match == "$$" {
			return "$"
		}

		var name string
		if len(match) > 1 && match[1] == '{' {
			name = match[2 : len(match)-1]
		} else {
			name = match[1:]
		}

		if v, ok := vars[name]; ok {
			return v
		}

		return ""
	})
}

// Shellescape wraps s in single quotes, escaping any embedded single quotes.
// This is POSIX-safe for paths, env values, and shell commands.
func Shellescape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellescape is the package-internal alias for Shellescape.
func shellescape(s string) string { return Shellescape(s) }
