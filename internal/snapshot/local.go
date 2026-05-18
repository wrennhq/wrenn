package snapshot

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

const (
	// Cloud Hypervisor snapshot files.
	CHConfigFile    = "config.json"
	CHMemRangesFile = "memory-ranges"
	CHStateFile     = "state.json"

	// Rootfs files.
	RootfsFileName = "rootfs.ext4"
	RootfsCowName  = "rootfs.cow"
	RootfsMetaName = "rootfs.meta"
)

// DirPath returns the snapshot directory for a given name.
func DirPath(baseDir, name string) string {
	return filepath.Join(baseDir, name)
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

// RootfsMeta records which base template a CoW file was created against
// and the VM resource config needed to restart the sampler on resume.
type RootfsMeta struct {
	BaseTemplate string `json:"base_template"`
	TemplateID   string `json:"template_id,omitempty"`
	VCPUs        int    `json:"vcpus,omitempty"`
	MemoryMB     int    `json:"memory_mb,omitempty"`
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
