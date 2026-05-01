package snapshot

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"
)

const (
	SnapFileName   = "snapfile"
	MemDiffName    = "memfile"
	MemHeaderName  = "memfile.header"
	RootfsFileName = "rootfs.ext4"
	RootfsCowName  = "rootfs.cow"
	RootfsMetaName = "rootfs.meta"
)

// DirPath returns the snapshot directory for a given name.
func DirPath(baseDir, name string) string {
	return filepath.Join(baseDir, name)
}

// SnapPath returns the path to the VM state snapshot file.
func SnapPath(baseDir, name string) string {
	return filepath.Join(DirPath(baseDir, name), SnapFileName)
}

// MemDiffPath returns the path to the compact memory diff file (legacy single-generation).
func MemDiffPath(baseDir, name string) string {
	return filepath.Join(DirPath(baseDir, name), MemDiffName)
}

// MemDiffPathForBuild returns the path to a specific generation's diff file.
// Format: memfile.{buildID}
func MemDiffPathForBuild(baseDir, name string, buildID uuid.UUID) string {
	return filepath.Join(DirPath(baseDir, name), fmt.Sprintf("memfile.%s", buildID.String()))
}

// MemHeaderPath returns the path to the memory mapping header file.
func MemHeaderPath(baseDir, name string) string {
	return filepath.Join(DirPath(baseDir, name), MemHeaderName)
}

// RootfsPath returns the path to the rootfs image.
func RootfsPath(baseDir, name string) string {
	return filepath.Join(DirPath(baseDir, name), RootfsFileName)
}

// CowPath returns the path to the rootfs CoW diff file.
func CowPath(baseDir, name string) string {
	return filepath.Join(DirPath(baseDir, name), RootfsCowName)
}

// MetaPath returns the path to the rootfs metadata file.
func MetaPath(baseDir, name string) string {
	return filepath.Join(DirPath(baseDir, name), RootfsMetaName)
}

// RootfsMeta records which base template a CoW file was created against.
type RootfsMeta struct {
	BaseTemplate string `json:"base_template"`
	TemplateID   string `json:"template_id,omitempty"`
}

// WriteMeta writes rootfs metadata to the snapshot directory.
func WriteMeta(baseDir, name string, meta *RootfsMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal rootfs meta: %w", err)
	}
	if err := os.WriteFile(MetaPath(baseDir, name), data, 0644); err != nil {
		return fmt.Errorf("write rootfs meta: %w", err)
	}
	return nil
}

// ReadMeta reads rootfs metadata from the snapshot directory.
func ReadMeta(baseDir, name string) (*RootfsMeta, error) {
	data, err := os.ReadFile(MetaPath(baseDir, name))
	if err != nil {
		return nil, fmt.Errorf("read rootfs meta: %w", err)
	}
	var meta RootfsMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal rootfs meta: %w", err)
	}
	return &meta, nil
}

// Exists reports whether a complete snapshot exists (all required files present).
// Supports both legacy (rootfs.ext4) and CoW-based (rootfs.cow + rootfs.meta) snapshots.
// Memory diff files can be either legacy "memfile" or generation-specific "memfile.{uuid}".
func Exists(baseDir, name string) bool {
	dir := DirPath(baseDir, name)

	// snapfile and header are always required.
	for _, f := range []string{SnapFileName, MemHeaderName} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return false
		}
	}

	// Check that at least one memfile exists (legacy or generation-specific).
	// We verify by reading the header and checking that referenced diff files exist.
	// Fall back to checking for the legacy memfile name if header can't be read.
	if _, err := os.Stat(filepath.Join(dir, MemDiffName)); err != nil {
		// No legacy memfile — check if any memfile.{uuid} exists by
		// looking for files matching the pattern.
		matches, _ := filepath.Glob(filepath.Join(dir, "memfile.*"))
		hasGenDiff := false
		for _, m := range matches {
			base := filepath.Base(m)
			if base != MemHeaderName {
				hasGenDiff = true
				break
			}
		}
		if !hasGenDiff {
			return false
		}
	}

	// Accept either rootfs.ext4 (legacy/template) or rootfs.cow + rootfs.meta (dm-snapshot).
	if _, err := os.Stat(filepath.Join(dir, RootfsFileName)); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, RootfsCowName)); err == nil {
		if _, err := os.Stat(filepath.Join(dir, RootfsMetaName)); err == nil {
			return true
		}
	}
	return false
}

// IsTemplate reports whether a template image directory exists (has rootfs.ext4).
func IsTemplate(baseDir, name string) bool {
	_, err := os.Stat(filepath.Join(DirPath(baseDir, name), RootfsFileName))
	return err == nil
}

// IsSnapshot reports whether a directory is a snapshot (has all snapshot files).
func IsSnapshot(baseDir, name string) bool {
	return Exists(baseDir, name)
}

// HasCow reports whether a snapshot uses CoW format (rootfs.cow + rootfs.meta)
// as opposed to legacy full rootfs (rootfs.ext4).
func HasCow(baseDir, name string) bool {
	dir := DirPath(baseDir, name)
	_, cowErr := os.Stat(filepath.Join(dir, RootfsCowName))
	_, metaErr := os.Stat(filepath.Join(dir, RootfsMetaName))
	return cowErr == nil && metaErr == nil
}

// ListDiffFiles returns a map of build ID → file path for all memory diff files
// referenced by the given header. Handles both the legacy "memfile" name
// (single-generation) and generation-specific "memfile.{uuid}" names.
func ListDiffFiles(baseDir, name string, header *Header) (map[string]string, error) {
	dir := DirPath(baseDir, name)
	result := make(map[string]string)

	for _, m := range header.Mapping {
		if m.BuildID == uuid.Nil {
			continue // zero-fill, no file needed
		}
		idStr := m.BuildID.String()
		if _, exists := result[idStr]; exists {
			continue
		}
		// Try generation-specific path first, fall back to legacy.
		genPath := filepath.Join(dir, fmt.Sprintf("memfile.%s", idStr))
		if _, err := os.Stat(genPath); err == nil {
			result[idStr] = genPath
			continue
		}
		legacyPath := filepath.Join(dir, MemDiffName)
		if _, err := os.Stat(legacyPath); err == nil {
			result[idStr] = legacyPath
			continue
		}
		return nil, fmt.Errorf("diff file not found for build %s", idStr)
	}
	return result, nil
}

// EnsureDir creates the snapshot directory if it doesn't exist.
func EnsureDir(baseDir, name string) error {
	dir := DirPath(baseDir, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create snapshot dir %s: %w", dir, err)
	}
	return nil
}

// Remove deletes the entire snapshot directory.
func Remove(baseDir, name string) error {
	return os.RemoveAll(DirPath(baseDir, name))
}

// DirSize returns the actual disk usage of all files in the snapshot directory.
// Uses block-based accounting (stat.Blocks * 512) so sparse files report only
// the blocks that are actually allocated, not their apparent size.
func DirSize(baseDir, name string) (int64, error) {
	var total int64
	dir := DirPath(baseDir, name)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if sys, ok := info.Sys().(*syscall.Stat_t); ok {
			// Blocks is in 512-byte units regardless of filesystem block size.
			total += sys.Blocks * 512
		} else {
			// Fallback to apparent size if syscall stat is unavailable.
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("calculate snapshot size: %w", err)
	}
	return total, nil
}
