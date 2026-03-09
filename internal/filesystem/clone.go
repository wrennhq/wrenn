package filesystem

import (
	"fmt"
	"os/exec"
)

// CloneRootfs creates a copy-on-write clone of the base rootfs image.
// Uses reflink if supported by the filesystem, falls back to regular copy.
func CloneRootfs(src, dst string) error {
	cmd := exec.Command("cp", "--reflink=auto", src, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cp --reflink=auto: %s: %w", string(out), err)
	}
	return nil
}
