package recipe

import (
	"fmt"
	"strings"
	"time"
)

// Kind identifies the instruction type in a recipe line.
type Kind int

const (
	KindRUN     Kind = iota // Execute a command and wait for it to exit.
	KindSTART               // Start a command in the background (non-blocking).
	KindENV                 // Set an environment variable for subsequent steps.
	KindWORKDIR             // Set the working directory for subsequent steps.
	KindUSER                // Switch the unix user for subsequent steps. (stub)
	KindCOPY                // Copy files into the sandbox. (stub)
)

// Step is the parsed representation of one recipe instruction.
type Step struct {
	Kind    Kind
	Raw     string        // original string, preserved for logging
	Shell   string        // KindRUN, KindSTART: the shell command text
	Timeout time.Duration // KindRUN: 0 means use caller's default
	Key     string        // KindENV: variable name; KindUSER: username
	Value   string        // KindENV: variable value
	Path    string        // KindWORKDIR: directory path
	Src     string        // KindCOPY: source path (relative to build archive)
	Dst     string        // KindCOPY: destination path inside sandbox
}

// ParseStep parses a single recipe instruction string into a Step.
// Instructions are Dockerfile-like: a keyword followed by arguments.
//
// Supported syntax:
//
//	RUN <cmd>                 — run command, wait for exit
//	RUN --timeout=<d> <cmd>  — run command with explicit timeout (e.g. --timeout=5m)
//	START <cmd>               — start command in background, return immediately
//	ENV <key>=<value>         — set environment variable
//	WORKDIR <path>            — set working directory
//	USER <name>               — not yet supported
//	COPY <src> <dst>          — not yet supported
func ParseStep(s string) (Step, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Step{}, fmt.Errorf("empty step")
	}

	// Split on first space to get the keyword.
	keyword, rest, _ := strings.Cut(s, " ")
	rest = strings.TrimSpace(rest)

	switch strings.ToUpper(keyword) {
	case "RUN":
		return parseRUN(s, rest)
	case "START":
		return parseSTART(s, rest)
	case "ENV":
		return parseENV(s, rest)
	case "WORKDIR":
		return parseWORKDIR(s, rest)
	case "USER":
		return parseUSER(s, rest)
	case "COPY":
		return parseCOPY(s, rest)
	default:
		return Step{}, fmt.Errorf("unknown instruction %q (expected RUN, START, ENV, WORKDIR, USER, or COPY)", keyword)
	}
}

// ParseRecipe parses all recipe lines, returning on the first error.
func ParseRecipe(lines []string) ([]Step, error) {
	steps := make([]Step, 0, len(lines))
	for i, line := range lines {
		st, err := ParseStep(line)
		if err != nil {
			return nil, fmt.Errorf("recipe line %d: %w", i+1, err)
		}
		steps = append(steps, st)
	}
	return steps, nil
}

func parseRUN(raw, rest string) (Step, error) {
	var timeout time.Duration
	if strings.HasPrefix(rest, "--timeout=") {
		rest = rest[len("--timeout="):]
		flag, cmd, found := strings.Cut(rest, " ")
		if !found || strings.TrimSpace(cmd) == "" {
			return Step{}, fmt.Errorf("RUN --timeout= flag has no command: %q", raw)
		}
		d, err := time.ParseDuration(flag)
		if err != nil {
			return Step{}, fmt.Errorf("RUN --timeout= invalid duration %q: %w", flag, err)
		}
		timeout = d
		rest = strings.TrimSpace(cmd)
	}
	if rest == "" {
		return Step{}, fmt.Errorf("RUN requires a command: %q", raw)
	}
	return Step{Kind: KindRUN, Raw: raw, Shell: rest, Timeout: timeout}, nil
}

func parseSTART(raw, rest string) (Step, error) {
	if rest == "" {
		return Step{}, fmt.Errorf("START requires a command: %q", raw)
	}
	return Step{Kind: KindSTART, Raw: raw, Shell: rest}, nil
}

func parseENV(raw, rest string) (Step, error) {
	key, value, found := strings.Cut(rest, "=")
	if !found {
		return Step{}, fmt.Errorf("ENV requires KEY=VALUE format: %q", raw)
	}
	if key == "" {
		return Step{}, fmt.Errorf("ENV key is empty: %q", raw)
	}
	return Step{Kind: KindENV, Raw: raw, Key: key, Value: value}, nil
}

func parseWORKDIR(raw, path string) (Step, error) {
	if path == "" {
		return Step{}, fmt.Errorf("WORKDIR requires a path: %q", raw)
	}
	return Step{Kind: KindWORKDIR, Raw: raw, Path: path}, nil
}

func parseUSER(raw, username string) (Step, error) {
	if username == "" {
		return Step{}, fmt.Errorf("USER requires a username: %q", raw)
	}
	// Validate: alphanumeric, hyphens, underscores only; must start with a letter or underscore.
	for i, c := range username {
		if i == 0 && !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_') {
			return Step{}, fmt.Errorf("USER username must start with a letter or underscore: %q", raw)
		}
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return Step{}, fmt.Errorf("USER username contains invalid character %q: %q", string(c), raw)
		}
	}
	return Step{Kind: KindUSER, Raw: raw, Key: username}, nil
}

func parseCOPY(raw, rest string) (Step, error) {
	if rest == "" {
		return Step{}, fmt.Errorf("COPY requires <src> <dst>: %q", raw)
	}
	src, dst, found := strings.Cut(rest, " ")
	dst = strings.TrimSpace(dst)
	if !found || dst == "" {
		return Step{}, fmt.Errorf("COPY requires <src> <dst>: %q", raw)
	}
	return Step{Kind: KindCOPY, Raw: raw, Src: src, Dst: dst}, nil
}
