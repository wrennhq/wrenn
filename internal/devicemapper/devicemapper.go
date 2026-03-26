// Package devicemapper provides device-mapper snapshot operations for
// copy-on-write rootfs management. Each sandbox gets a dm-snapshot backed
// by a shared read-only loop device (the base template image) and a
// per-sandbox sparse CoW file that stores only modified blocks.
package devicemapper

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ChunkSize is the dm-snapshot chunk size in 512-byte sectors.
	// 8 sectors = 4KB, matching the standard page/block size.
	ChunkSize = 8
)

// loopEntry tracks a loop device and its reference count.
type loopEntry struct {
	device   string // e.g., /dev/loop0
	refcount int
}

// LoopRegistry manages loop devices for base template images.
// Each unique image path gets one read-only loop device, shared
// across all sandboxes using that template. Reference counting
// ensures the loop device is released when no sandboxes use it.
type LoopRegistry struct {
	mu      sync.Mutex
	entries map[string]*loopEntry // imagePath → loopEntry
}

// NewLoopRegistry creates a new loop device registry.
func NewLoopRegistry() *LoopRegistry {
	return &LoopRegistry{
		entries: make(map[string]*loopEntry),
	}
}

// Acquire returns a read-only loop device for the given image path.
// If one already exists, its refcount is incremented. Otherwise a new
// loop device is created via losetup.
func (r *LoopRegistry) Acquire(imagePath string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.entries[imagePath]; ok {
		e.refcount++
		slog.Debug("loop device reused", "image", imagePath, "device", e.device, "refcount", e.refcount)
		return e.device, nil
	}

	dev, err := losetupCreate(imagePath)
	if err != nil {
		return "", fmt.Errorf("losetup %s: %w", imagePath, err)
	}

	r.entries[imagePath] = &loopEntry{device: dev, refcount: 1}
	slog.Info("loop device created", "image", imagePath, "device", dev)
	return dev, nil
}

// Release decrements the refcount for the given image path.
// When the refcount reaches zero, the loop device is detached.
func (r *LoopRegistry) Release(imagePath string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.entries[imagePath]
	if !ok {
		return
	}

	e.refcount--
	if e.refcount <= 0 {
		if err := losetupDetach(e.device); err != nil {
			slog.Warn("losetup detach failed", "device", e.device, "error", err)
		}
		delete(r.entries, imagePath)
		slog.Info("loop device released", "image", imagePath, "device", e.device)
	}
}

// ReleaseAll detaches all loop devices. Used during shutdown.
func (r *LoopRegistry) ReleaseAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for path, e := range r.entries {
		if err := losetupDetach(e.device); err != nil {
			slog.Warn("losetup detach failed", "device", e.device, "error", err)
		}
		delete(r.entries, path)
	}
}

// SnapshotDevice holds the state for a single dm-snapshot device.
type SnapshotDevice struct {
	Name       string // dm device name, e.g., "wrenn-sb-a1b2c3d4"
	DevicePath string // /dev/mapper/<Name>
	CowPath    string // path to the sparse CoW file
	CowLoopDev string // loop device for the CoW file
}

// CreateSnapshot sets up a new dm-snapshot device.
//
// It creates a sparse CoW file, attaches it as a loop device, and creates
// a device-mapper snapshot target combining the read-only origin with the
// writable CoW layer.
//
// The origin loop device must already exist (from LoopRegistry.Acquire).
func CreateSnapshot(name, originLoopDev, cowPath string, originSizeBytes, cowSizeBytes int64) (*SnapshotDevice, error) {
	// Create sparse CoW file. The logical size limits how many blocks can be
	// modified; because the file is sparse, only written blocks use real disk.
	if err := createSparseFile(cowPath, cowSizeBytes); err != nil {
		return nil, fmt.Errorf("create cow file: %w", err)
	}

	cowLoopDev, err := losetupCreateRW(cowPath)
	if err != nil {
		os.Remove(cowPath)
		return nil, fmt.Errorf("losetup cow: %w", err)
	}

	// The dm-snapshot virtual device size must match the origin — the snapshot
	// target maps 1:1 onto origin sectors. The CoW file just needs enough
	// space to store all modified blocks (it's sparse, so 20GB costs nothing).
	sectors := originSizeBytes / 512
	if err := dmsetupCreate(name, originLoopDev, cowLoopDev, sectors); err != nil {
		if detachErr := losetupDetach(cowLoopDev); detachErr != nil {
			slog.Warn("cow losetup detach failed during cleanup", "device", cowLoopDev, "error", detachErr)
		}
		os.Remove(cowPath)
		return nil, fmt.Errorf("dmsetup create: %w", err)
	}

	devPath := "/dev/mapper/" + name

	slog.Info("dm-snapshot created",
		"name", name,
		"device", devPath,
		"origin", originLoopDev,
		"cow", cowPath,
	)

	return &SnapshotDevice{
		Name:       name,
		DevicePath: devPath,
		CowPath:    cowPath,
		CowLoopDev: cowLoopDev,
	}, nil
}

// RestoreSnapshot re-attaches a dm-snapshot from an existing persistent CoW file.
// The CoW file must have been created with the persistent (P) flag and still
// contain valid dm-snapshot metadata.
func RestoreSnapshot(ctx context.Context, name, originLoopDev, cowPath string, originSizeBytes int64) (*SnapshotDevice, error) {
	// Defensively remove a stale device with the same name. This can happen
	// if a previous pause failed to clean up the dm device (e.g. "device busy").
	if dmDeviceExists(name) {
		slog.Warn("removing stale dm device before restore", "name", name)
		if err := dmsetupRemove(ctx, name); err != nil {
			return nil, fmt.Errorf("remove stale device %s: %w", name, err)
		}
	}

	cowLoopDev, err := losetupCreateRW(cowPath)
	if err != nil {
		return nil, fmt.Errorf("losetup cow: %w", err)
	}

	sectors := originSizeBytes / 512
	if err := dmsetupCreate(name, originLoopDev, cowLoopDev, sectors); err != nil {
		if detachErr := losetupDetach(cowLoopDev); detachErr != nil {
			slog.Warn("cow losetup detach failed during cleanup", "device", cowLoopDev, "error", detachErr)
		}
		return nil, fmt.Errorf("dmsetup create: %w", err)
	}

	devPath := "/dev/mapper/" + name

	slog.Info("dm-snapshot restored",
		"name", name,
		"device", devPath,
		"origin", originLoopDev,
		"cow", cowPath,
	)

	return &SnapshotDevice{
		Name:       name,
		DevicePath: devPath,
		CowPath:    cowPath,
		CowLoopDev: cowLoopDev,
	}, nil
}

// RemoveSnapshot tears down a dm-snapshot device and its CoW loop device.
// The CoW file is NOT deleted — the caller decides whether to keep or remove it.
func RemoveSnapshot(ctx context.Context, dev *SnapshotDevice) error {
	if err := dmsetupRemove(ctx, dev.Name); err != nil {
		return fmt.Errorf("dmsetup remove %s: %w", dev.Name, err)
	}

	if err := losetupDetach(dev.CowLoopDev); err != nil {
		slog.Warn("cow losetup detach failed", "device", dev.CowLoopDev, "error", err)
	}

	slog.Info("dm-snapshot removed", "name", dev.Name)
	return nil
}

// FlattenSnapshot reads the full contents of a dm-snapshot device and writes
// it to a new file. This merges the base image + CoW changes into a standalone
// rootfs image suitable for use as a new template.
func FlattenSnapshot(dmDevPath, outputPath string) error {
	cmd := exec.Command("dd",
		"if="+dmDevPath,
		"of="+outputPath,
		"bs=4M",
		"status=none",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(outputPath)
		return fmt.Errorf("dd flatten: %s: %w", string(out), err)
	}
	return nil
}

// OriginSizeBytes returns the size in bytes of a loop device's backing file.
func OriginSizeBytes(loopDev string) (int64, error) {
	// blockdev --getsize64 returns size in bytes.
	out, err := exec.Command("blockdev", "--getsize64", loopDev).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("blockdev --getsize64 %s: %s: %w", loopDev, strings.TrimSpace(string(out)), err)
	}
	s := strings.TrimSpace(string(out))
	return strconv.ParseInt(s, 10, 64)
}

// CleanupStaleDevices removes any device-mapper devices matching the
// "wrenn-" prefix that may have been left behind by a previous agent
// instance that crashed or was killed. Should be called at agent startup.
func CleanupStaleDevices() {
	out, err := exec.Command("dmsetup", "ls", "--target", "snapshot").CombinedOutput()
	if err != nil {
		slog.Debug("dmsetup ls failed (may be normal if no devices exist)", "error", err)
		return
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || line == "No devices found" {
			continue
		}
		// dmsetup ls output format: "name\t(major:minor)"
		name, _, _ := strings.Cut(line, "\t")
		if !strings.HasPrefix(name, "wrenn-") {
			continue
		}

		slog.Warn("removing stale dm-snapshot device", "name", name)
		if err := dmsetupRemove(context.Background(), name); err != nil {
			slog.Warn("failed to remove stale device", "name", name, "error", err)
		}
	}
}

// --- low-level helpers ---

// losetupCreate attaches a file as a read-only loop device.
func losetupCreate(imagePath string) (string, error) {
	out, err := exec.Command("losetup", "--read-only", "--find", "--show", imagePath).Output()
	if err != nil {
		return "", fmt.Errorf("losetup --read-only: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// losetupCreateRW attaches a file as a read-write loop device.
func losetupCreateRW(path string) (string, error) {
	out, err := exec.Command("losetup", "--find", "--show", path).Output()
	if err != nil {
		return "", fmt.Errorf("losetup: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// losetupDetach detaches a loop device.
func losetupDetach(dev string) error {
	return exec.Command("losetup", "-d", dev).Run()
}

// dmsetupCreate creates a dm-snapshot device with persistent metadata.
func dmsetupCreate(name, originDev, cowDev string, sectors int64) error {
	// Table format: <start> <size> snapshot <origin> <cow> P <chunk_size>
	// P = persistent — CoW metadata survives dmsetup remove.
	table := fmt.Sprintf("0 %d snapshot %s %s P %d", sectors, originDev, cowDev, ChunkSize)
	cmd := exec.Command("dmsetup", "create", name, "--table", table)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// dmDeviceExists checks whether a device-mapper device with the given name exists.
func dmDeviceExists(name string) bool {
	return exec.Command("dmsetup", "info", name).Run() == nil
}

// dmsetupRemove removes a device-mapper device, retrying on transient
// "device busy" errors that occur when the kernel hasn't fully released
// the device after a Firecracker process exits.
func dmsetupRemove(ctx context.Context, name string) error {
	var lastErr error
	for attempt := range 5 {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("context cancelled while retrying dmsetup remove %s: %w", name, lastErr)
			case <-time.After(200 * time.Millisecond):
			}
		}
		cmd := exec.CommandContext(ctx, "dmsetup", "remove", name)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		// If the context was cancelled, the process was killed and its
		// output is unreliable. Return the context error directly so
		// callers can distinguish cancellation from a real dm failure.
		if ctx.Err() != nil {
			return fmt.Errorf("dmsetup remove %s: %w", name, ctx.Err())
		}
		outStr := strings.TrimSpace(string(out))
		lastErr = fmt.Errorf("%s: %w", outStr, err)
		// Only retry on transient "busy" errors. Other failures
		// (device not found, permission denied) are permanent.
		if !strings.Contains(outStr, "Device or resource busy") {
			return lastErr
		}
		slog.Debug("dmsetup remove retry", "name", name, "attempt", attempt+1, "error", lastErr)
	}
	return lastErr
}

// createSparseFile creates a sparse file of the given size.
func createSparseFile(path string, sizeBytes int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := f.Truncate(sizeBytes); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}
