package sandbox

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// DefaultDiskSizeMB is the standard disk size for base images. Images smaller
// than this are expanded at startup so that dm-snapshot sandboxes see the full
// size without per-sandbox copies. The expansion is sparse — only metadata
// changes; no physical disk is consumed beyond the original content.
const DefaultDiskSizeMB = 20480 // 20 GB

// EnsureImageSizes walks the images directory and expands any rootfs.ext4 that
// is smaller than the target size. This is idempotent: images already at or
// above the target size are left untouched. Should be called once at host agent
// startup before any sandboxes are created.
func EnsureImageSizes(imagesDir string, targetMB int) error {
	if targetMB <= 0 {
		targetMB = DefaultDiskSizeMB
	}
	targetBytes := int64(targetMB) * 1024 * 1024

	entries, err := os.ReadDir(imagesDir)
	if err != nil {
		return fmt.Errorf("read images dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rootfs := filepath.Join(imagesDir, entry.Name(), "rootfs.ext4")
		info, err := os.Stat(rootfs)
		if err != nil {
			continue // not every template dir has a rootfs.ext4
		}

		if info.Size() >= targetBytes {
			continue // already large enough
		}

		slog.Info("expanding base image",
			"template", entry.Name(),
			"from_mb", info.Size()/(1024*1024),
			"to_mb", targetMB,
		)

		// Expand the file (sparse — instant, no physical disk used).
		if err := os.Truncate(rootfs, targetBytes); err != nil {
			return fmt.Errorf("truncate %s: %w", rootfs, err)
		}

		// Check filesystem before resize.
		if out, err := exec.Command("e2fsck", "-fy", rootfs).CombinedOutput(); err != nil {
			// e2fsck returns 1 if it fixed errors, which is fine.
			if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() > 1 {
				return fmt.Errorf("e2fsck %s: %s: %w", rootfs, string(out), err)
			}
		}

		// Grow the ext4 filesystem to fill the new file size.
		if out, err := exec.Command("resize2fs", rootfs).CombinedOutput(); err != nil {
			return fmt.Errorf("resize2fs %s: %s: %w", rootfs, string(out), err)
		}

		slog.Info("base image expanded", "template", entry.Name(), "size_mb", targetMB)
	}

	return nil
}
