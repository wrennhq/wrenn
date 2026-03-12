package snapshot

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	SnapFileName   = "snapfile"
	MemDiffName    = "memfile"
	MemHeaderName  = "memfile.header"
	RootfsFileName = "rootfs.ext4"
)

// DirPath returns the snapshot directory for a given name.
func DirPath(baseDir, name string) string {
	return filepath.Join(baseDir, name)
}

// SnapPath returns the path to the VM state snapshot file.
func SnapPath(baseDir, name string) string {
	return filepath.Join(DirPath(baseDir, name), SnapFileName)
}

// MemDiffPath returns the path to the compact memory diff file.
func MemDiffPath(baseDir, name string) string {
	return filepath.Join(DirPath(baseDir, name), MemDiffName)
}

// MemHeaderPath returns the path to the memory mapping header file.
func MemHeaderPath(baseDir, name string) string {
	return filepath.Join(DirPath(baseDir, name), MemHeaderName)
}

// RootfsPath returns the path to the rootfs image.
func RootfsPath(baseDir, name string) string {
	return filepath.Join(DirPath(baseDir, name), RootfsFileName)
}

// Exists reports whether a complete snapshot exists (all required files present).
func Exists(baseDir, name string) bool {
	dir := DirPath(baseDir, name)
	for _, f := range []string{SnapFileName, MemDiffName, MemHeaderName, RootfsFileName} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return false
		}
	}
	return true
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

// DirSize returns the total byte size of all files in the snapshot directory.
func DirSize(baseDir, name string) (int64, error) {
	var total int64
	dir := DirPath(baseDir, name)

	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
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
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("calculate snapshot size: %w", err)
	}
	return total, nil
}
