package recipe

import "strings"

// ExecContext holds mutable state that persists across recipe steps.
// It is initialized empty and updated by ENV and WORKDIR steps.
type ExecContext struct {
	WorkDir string
	EnvVars map[string]string
}

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

// shellescape wraps s in single quotes, escaping any embedded single quotes.
// This is POSIX-safe for paths, env values, and shell commands.
func shellescape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
