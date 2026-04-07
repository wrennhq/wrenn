package recipe

import (
	"regexp"
	"strings"
)

// ExecContext holds mutable state that persists across recipe steps.
// It is initialized empty and updated by ENV and WORKDIR steps.
type ExecContext struct {
	WorkDir string
	EnvVars map[string]string
}

// This regex matches:
// 1. $$
// 2. ${ANY_WORD}
// 3. $ANY_WORD
var envRegex = regexp.MustCompile(`\$\$(\$)?|\$\{([a-zA-Z0-9_]+)\}|\$([a-zA-Z0-9_]+)`)

// WrappedCommand returns the full shell command for a RUN step with context
// applied. The result is passed as the argument to /bin/sh -c.
//
// If WORKDIR and/or ENV are set, they are prepended as a shell preamble:
//
//	cd '/the/dir' && KEY='val' /bin/sh -c 'original command'
func (c *ExecContext) WrappedCommand(cmd string) string {
	prefix := c.shellPrefix()
	if prefix == "" {
		return cmd
	}
	return prefix + "/bin/sh -c " + shellescape(cmd)
}

// StartCommand returns the shell command for a START step. The process is
// launched in the background via nohup so that the outer shell exits
// immediately, allowing the build to continue. stdout/stderr of the
// background process are discarded (the process keeps running in the VM).
//
// Multiple START steps can be issued to run several background processes
// simultaneously before a healthcheck is evaluated.
func (c *ExecContext) StartCommand(cmd string) string {
	prefix := c.shellPrefix()
	return prefix + "nohup /bin/sh -c " + shellescape(cmd) + " >/dev/null 2>&1 &"
}

// shellPrefix builds the "cd ... && KEY=val " preamble for a shell command.
// Returns an empty string when no context is set.
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
	for k, v := range c.EnvVars {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(shellescape(v))
		sb.WriteByte(' ')
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
		if match[1] == '{' {
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

// shellescape wraps s in single quotes, escaping any embedded single quotes.
// This is POSIX-safe for paths, env values, and shell commands.
func shellescape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
