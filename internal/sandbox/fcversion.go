package sandbox

import (
	"fmt"
	"os/exec"
	"strings"
)

// DetectFirecrackerVersion runs the firecracker binary with --version and
// parses the semver from the output (e.g. "Firecracker v1.14.1" → "1.14.1").
func DetectFirecrackerVersion(binaryPath string) (string, error) {
	out, err := exec.Command(binaryPath, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", binaryPath, err)
	}

	// Output is typically "Firecracker v1.14.1\n" or similar.
	line := strings.TrimSpace(string(out))
	for _, field := range strings.Fields(line) {
		v := strings.TrimPrefix(field, "v")
		if v != field || strings.Contains(field, ".") {
			// Either had a "v" prefix or contains a dot — likely the version.
			if strings.Count(v, ".") >= 1 {
				return v, nil
			}
		}
	}

	return "", fmt.Errorf("could not parse version from firecracker output: %q", line)
}
