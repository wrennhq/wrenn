package recipe

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HealthcheckConfig holds the parsed configuration for a build healthcheck.
// A healthcheck is a shell command that is executed repeatedly inside the
// sandbox until it succeeds or the retry/timeout budget is exhausted.
//
// Retries of 0 means unlimited retries (bounded only by the overall deadline)
type HealthcheckConfig struct {
	Cmd         string
	Interval    time.Duration
	Timeout     time.Duration
	StartPeriod time.Duration
	Retries     int // 0 = unlimited
}

// ParseHealthcheck parses a healthcheck string with optional flag prefix into
// a HealthcheckConfig. The syntax is:
//
// [--interval=<duration>] [--timeout=<duration>] [--start-period=<duration>]
// [--retries=<n>] <command>
//
// Flags must use the form --flag=value. The first token that does not start
// with "--" and everything after it is treated as the command. Defaults:
// interval=3s, timeout=10s, start-period=0, retries=0 (unlimited)
func ParseHealthcheck(s string) (HealthcheckConfig, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return HealthcheckConfig{}, fmt.Errorf("empty healthcheck")
	}

	hc := HealthcheckConfig{
		Interval: 3 * time.Second,
		Timeout:  10 * time.Second,
	}

	tokens := strings.Fields(s)
	cmdIndex := -1

	for i, token := range tokens {
		if !strings.HasPrefix(token, "--") {
			cmdIndex = i
			break
		}

		parts := strings.SplitN(token, "=", 2)
		if len(parts) != 2 {
			return HealthcheckConfig{}, fmt.Errorf("malformed flag (missing '='): %q", token)
		}

		key, val := parts[0], parts[1]
		switch key {
		case "--interval":
			d, err := time.ParseDuration(val)
			if err != nil {
				return HealthcheckConfig{}, fmt.Errorf("parse interval: %w", err)
			}
			hc.Interval = d
		case "--timeout":
			d, err := time.ParseDuration(val)
			if err != nil {
				return HealthcheckConfig{}, fmt.Errorf("parse timeout: %w", err)
			}
			hc.Timeout = d
		case "--start-period":
			d, err := time.ParseDuration(val)
			if err != nil {
				return HealthcheckConfig{}, fmt.Errorf("parse start period: %w", err)
			}
			hc.StartPeriod = d
		case "--retries":
			r, err := strconv.Atoi(val)
			if err != nil {
				return HealthcheckConfig{}, fmt.Errorf("parse retries: %w", err)
			}
			hc.Retries = r
		default:
			return HealthcheckConfig{}, fmt.Errorf("unknown healthcheck flag: %q", token)
		}
	}

	if cmdIndex == -1 {
		return HealthcheckConfig{}, fmt.Errorf("healthcheck has no command")
	}

	hc.Cmd = strings.Join(tokens[cmdIndex:], " ")
	return hc, nil
}
