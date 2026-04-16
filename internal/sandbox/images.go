package sandbox

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"git.omukk.dev/wrenn/wrenn/internal/layout"
	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

// DefaultDiskSizeMB is the standard disk size for base images. Images smaller
// than this are expanded at startup so that dm-snapshot sandboxes see the full
// size without per-sandbox copies. The expansion is sparse — only metadata
// changes; no physical disk is consumed beyond the original content.
const DefaultDiskSizeMB = 5120 // 5 GB

// EnsureImageSizes walks template directories and expands any rootfs.ext4 that
// is smaller than the target size. This is idempotent: images already at or
// above the target size are left untouched. Should be called once at host agent
// startup before any sandboxes are created.
func EnsureImageSizes(wrennDir string, targetMB int) error {
	if targetMB <= 0 {
		targetMB = DefaultDiskSizeMB
	}
	targetBytes := int64(targetMB) * 1024 * 1024

	// Expand the built-in minimal image.
	minimalRootfs := layout.TemplateRootfs(wrennDir, id.PlatformTeamID, id.MinimalTemplateID)
	if err := expandImage(minimalRootfs, targetBytes, targetMB); err != nil {
		return err
	}

	// Walk teams/{teamDir}/{templateDir}/rootfs.ext4 two levels deep.
	teamsDir := layout.TeamsDir(wrennDir)
	teamEntries, err := os.ReadDir(teamsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // teams dir doesn't exist yet — nothing to expand
		}
		return fmt.Errorf("read teams dir: %w", err)
	}

	for _, teamEntry := range teamEntries {
		if !teamEntry.IsDir() {
			continue
		}
		teamPath := filepath.Join(teamsDir, teamEntry.Name())
		templateEntries, err := os.ReadDir(teamPath)
		if err != nil {
			continue
		}
		for _, tmplEntry := range templateEntries {
			if !tmplEntry.IsDir() {
				continue
			}
			rootfs := filepath.Join(teamPath, tmplEntry.Name(), "rootfs.ext4")
			if err := expandImage(rootfs, targetBytes, targetMB); err != nil {
				return err
			}
		}
	}

	return nil
}

// ParseSizeToMB parses a human-readable size string into megabytes.
// Supported suffixes: G, Gi (gibibytes), M, Mi (mebibytes).
// Examples: "5G" → 5120, "2Gi" → 2048, "1000M" → 1000, "512Mi" → 512.
func ParseSizeToMB(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	// Find where the numeric part ends.
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid size %q: no numeric value", s)
	}

	numStr := s[:i]
	suffix := strings.TrimSpace(s[i:])

	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}

	switch suffix {
	case "G", "Gi":
		return int(num * 1024), nil
	case "M", "Mi", "":
		return int(num), nil
	default:
		return 0, fmt.Errorf("invalid size %q: unknown suffix %q (use G, Gi, M, or Mi)", s, suffix)
	}
}

// ShrinkMinimalImage shrinks the built-in minimal rootfs back to its minimum
// size using resize2fs -M. This is the inverse of EnsureImageSizes and should
// be called during graceful shutdown so the image is stored compactly on disk.
func ShrinkMinimalImage(wrennDir string) {
	minimalRootfs := layout.TemplateRootfs(wrennDir, id.PlatformTeamID, id.MinimalTemplateID)
	shrinkImage(minimalRootfs)
}

// shrinkImage shrinks a single rootfs image to its minimum size.
func shrinkImage(rootfs string) {
	if _, err := os.Stat(rootfs); err != nil {
		return
	}

	slog.Info("shrinking base image", "path", rootfs)

	if out, err := exec.Command("e2fsck", "-fy", rootfs).CombinedOutput(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() > 1 {
			slog.Warn("e2fsck before shrink failed", "path", rootfs, "output", string(out), "error", err)
			return
		}
	}

	if out, err := exec.Command("resize2fs", "-M", rootfs).CombinedOutput(); err != nil {
		slog.Warn("resize2fs -M failed", "path", rootfs, "output", string(out), "error", err)
		return
	}

	slog.Info("base image shrunk", "path", rootfs)
}

// expandImage expands a single rootfs image if it is smaller than targetBytes.
func expandImage(rootfs string, targetBytes int64, targetMB int) error {
	info, err := os.Stat(rootfs)
	if err != nil {
		return nil // not every template dir has a rootfs.ext4
	}

	if info.Size() >= targetBytes {
		return nil // already large enough
	}

	slog.Info("expanding base image",
		"path", rootfs,
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

	slog.Info("base image expanded", "path", rootfs, "size_mb", targetMB)
	return nil
}
