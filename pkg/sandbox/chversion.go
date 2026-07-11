package sandbox

import (
	"fmt"
	"os/exec"
	"strings"
)

// DetectCHVersion runs the cloud-hypervisor binary with --version and
// parses the semver from the output (e.g. "cloud-hypervisor v43.0" → "43.0").
func DetectCHVersion(binaryPath string) (string, error) {
	out, err := exec.Command(binaryPath, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", binaryPath, err)
	}

	line := strings.TrimSpace(string(out))
	for field := range strings.FieldsSeq(line) {
		v := strings.TrimPrefix(field, "v")
		if v != field || strings.Contains(field, ".") {
			if strings.Count(v, ".") >= 1 {
				return v, nil
			}
		}
	}

	return "", fmt.Errorf("could not parse version from cloud-hypervisor output: %q", line)
}
