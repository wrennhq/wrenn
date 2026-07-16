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
		if err := losetupDetachRetry(e.device); err != nil {
			slog.Error("losetup detach failed, loop device leaked", "device", e.device, "image", imagePath, "error", err)
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
		if err := losetupDetachRetry(e.device); err != nil {
			slog.Error("losetup detach failed during shutdown", "device", e.device, "image", path, "error", err)
		}
		delete(r.entries, path)
	}
}

// SnapshotDevice holds the state for a single dm-snapshot device.
type SnapshotDevice struct {
	Name       string // dm device name, e.g., "wrenn-cl-a1b2c3d4"
	DevicePath string // /dev/mapper/<Name>
	CowPath    string // path to the sparse CoW file
	CowLoopDev string // loop device for the CoW file
}

// attachCowAndCreate attaches a CoW file as a loop device, creates the
// dm-snapshot target, and returns the assembled SnapshotDevice. On failure
// it detaches the CoW loop device before returning.
func attachCowAndCreate(name, originLoopDev, cowPath string, originSizeBytes int64) (*SnapshotDevice, error) {
	cowLoopDev, err := losetupCreateRW(cowPath)
	if err != nil {
		return nil, fmt.Errorf("losetup cow: %w", err)
	}

	sectors := originSizeBytes / 512
	if err := dmsetupCreate(name, originLoopDev, cowLoopDev, sectors); err != nil {
		if detachErr := losetupDetachRetry(cowLoopDev); detachErr != nil {
			slog.Error("cow losetup detach failed during cleanup, loop device leaked", "device", cowLoopDev, "error", detachErr)
		}
		return nil, fmt.Errorf("dmsetup create: %w", err)
	}

	return &SnapshotDevice{
		Name:       name,
		DevicePath: "/dev/mapper/" + name,
		CowPath:    cowPath,
		CowLoopDev: cowLoopDev,
	}, nil
}

// CreateSnapshot sets up a new dm-snapshot device.
//
// It creates a sparse CoW file, attaches it as a loop device, and creates
// a device-mapper snapshot target combining the read-only origin with the
// writable CoW layer.
//
// The origin loop device must already exist (from LoopRegistry.Acquire).
func CreateSnapshot(name, originLoopDev, cowPath string, originSizeBytes, cowSizeBytes int64) (*SnapshotDevice, error) {
	if err := createSparseFile(cowPath, cowSizeBytes); err != nil {
		return nil, fmt.Errorf("create cow file: %w", err)
	}

	dev, err := attachCowAndCreate(name, originLoopDev, cowPath, originSizeBytes)
	if err != nil {
		os.Remove(cowPath)
		return nil, err
	}

	slog.Info("dm-snapshot created",
		"name", name,
		"device", dev.DevicePath,
		"origin", originLoopDev,
		"cow", cowPath,
	)

	return dev, nil
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

	dev, err := attachCowAndCreate(name, originLoopDev, cowPath, originSizeBytes)
	if err != nil {
		return nil, err
	}

	slog.Info("dm-snapshot restored",
		"name", name,
		"device", dev.DevicePath,
		"origin", originLoopDev,
		"cow", cowPath,
	)

	return dev, nil
}

// RemoveSnapshot tears down a dm-snapshot device and its CoW loop device.
// The CoW file is NOT deleted — the caller decides whether to keep or remove it.
//
// The CoW loop is detached even when the dm removal fails (busy device):
// callers treat a failed removal as fatal teardown and typically delete the
// CoW backing file next, which would orphan a still-attached loop forever
// (losetup can no longer find it by file). Detaching first is always safe —
// while dm still references the loop the kernel just defers the release via
// autoclear until that reference drops.
func RemoveSnapshot(ctx context.Context, dev *SnapshotDevice) error {
	if err := dmsetupRemove(ctx, dev.Name); err != nil {
		if derr := losetupDetachRetry(dev.CowLoopDev); derr != nil {
			slog.Warn("cow loop detach after failed dm remove",
				"device", dev.CowLoopDev, "name", dev.Name, "error", derr)
		}
		return fmt.Errorf("dmsetup remove %s: %w", dev.Name, err)
	}

	if err := losetupDetachRetry(dev.CowLoopDev); err != nil {
		return fmt.Errorf("detach cow loop %s: %w", dev.CowLoopDev, err)
	}

	slog.Info("dm-snapshot removed", "name", dev.Name)
	return nil
}

// ReattachSnapshot reconstructs the SnapshotDevice handle for a dm-snapshot
// that was created by an earlier process and is still live in the kernel. No
// device-mapper state is modified — the existing dm device is verified and
// the CoW loop device is rediscovered from its backing file, so a later
// RemoveSnapshot can detach it as usual.
func ReattachSnapshot(name, cowPath string) (*SnapshotDevice, error) {
	if !dmDeviceExists(name) {
		return nil, fmt.Errorf("dm device %s does not exist", name)
	}
	cowLoopDev, err := losetupFindByFile(cowPath)
	if err != nil {
		return nil, fmt.Errorf("find cow loop for %s: %w", cowPath, err)
	}
	return &SnapshotDevice{
		Name:       name,
		DevicePath: "/dev/mapper/" + name,
		CowPath:    cowPath,
		CowLoopDev: cowLoopDev,
	}, nil
}

// FlattenSnapshot reads the full contents of a dm-snapshot device and writes
// it to a new file. This merges the base image + CoW changes into a standalone
// rootfs image suitable for use as a new template.
func FlattenSnapshot(dmDevPath, outputPath string) error {
	cmd := exec.Command("dd",
		"if="+dmDevPath,
		"of="+outputPath,
		"bs=4M",
		"conv=sparse",
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

// LogLoopState enumerates currently-attached loop devices that back wrenn
// rootfs images and logs them at INFO. Diagnostic only — meant to be called
// once at agent startup so leaked loop attachments from a prior crash are
// visible in the journal before the LoopRegistry starts refcounting.
func LogLoopState() {
	out, err := exec.Command("losetup", "-l", "--noheadings", "--output", "NAME,BACK-FILE").CombinedOutput()
	if err != nil {
		slog.Debug("losetup -l failed", "error", err)
		return
	}
	wrennCount := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if !strings.Contains(line, "/var/lib/wrenn/") {
			continue
		}
		wrennCount++
		slog.Info("pre-existing loop attachment", "entry", strings.TrimSpace(line))
	}
	if wrennCount == 0 {
		slog.Info("no pre-existing wrenn loop attachments")
	}
}

// DetachLoopsByFile best-effort detaches every loop device backed by path.
//
// Safety net for teardown paths that are about to delete a backing file whose
// loop may still be attached (e.g. RemoveSnapshot failed on a busy dm device,
// or startup cleanup removed the dm device before the CoW loop was found).
// Deleting the file first orphans the loop permanently — losetup can no
// longer find it by file, so no later sweep can reclaim it. Detaching first
// always works: if device-mapper still references the loop, the kernel defers
// the release via autoclear until that reference drops.
func DetachLoopsByFile(path string) {
	out, err := exec.Command("losetup", "-j", path, "--output", "NAME", "--noheadings").Output()
	if err != nil {
		slog.Debug("losetup -j failed", "path", path, "error", err)
		return
	}
	for _, dev := range strings.Fields(strings.TrimSpace(string(out))) {
		if err := losetupDetachRetry(dev); err != nil {
			slog.Warn("loop detach by file failed", "device", dev, "path", path, "error", err)
		} else {
			slog.Info("detached leftover loop device", "device", dev, "path", path)
		}
	}
}

// --- low-level helpers ---

// losetupCreate attaches a file as a read-only loop device, reusing an existing
// attachment for the same backing file when one is already present.
//
// `losetup --find` always allocates a NEW device even when the file is already
// mapped. Within a single long-lived process the LoopRegistry hides this by
// caching the device per image path, but a daemonless caller runs many
// short-lived processes, each with its own registry: when a later process
// re-attaches a sandbox created by an earlier one, a naive --find would attach a
// second loop to the same origin image while the dm-snapshot still references
// the first. That later process then balances its refcount by detaching only
// its own (second) loop, orphaning the original — a loop leak that accumulates
// one device per template per re-attach/destroy cycle. Reusing the existing
// attachment keeps the kernel's loop set 1:1 with backing files, so the device a
// caller releases is the same one the dm origin depends on. Mirrors how
// ReattachSnapshot already rediscovers the CoW loop via losetupFindByFile.
func losetupCreate(imagePath string) (string, error) {
	// Reuse when exactly one loop already backs the image. losetupFindByFile
	// errors on both "none" and "more than one"; either way fall through to
	// attach a fresh device (the multi-attachment case only arises from loops
	// leaked before this reuse logic existed, and is reclaimed separately).
	if dev, err := losetupFindByFile(imagePath); err == nil {
		return dev, nil
	}
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

// losetupFindByFile returns the loop device backed by the given file.
// Errors if none or more than one is attached.
func losetupFindByFile(path string) (string, error) {
	out, err := exec.Command("losetup", "-j", path, "--output", "NAME", "--noheadings").Output()
	if err != nil {
		return "", fmt.Errorf("losetup -j: %w", err)
	}
	devs := strings.Fields(strings.TrimSpace(string(out)))
	if len(devs) == 0 {
		return "", fmt.Errorf("no loop device attached to %s", path)
	}
	if len(devs) > 1 {
		return "", fmt.Errorf("multiple loop devices attached to %s: %v", path, devs)
	}
	return devs[0], nil
}

// losetupDetach detaches a loop device.
func losetupDetach(dev string) error {
	return exec.Command("losetup", "-d", dev).Run()
}

// losetupDetachRetry detaches a loop device with retries for transient
// "device busy" errors (kernel may still hold references briefly after
// dm-snapshot removal).
func losetupDetachRetry(dev string) error {
	var lastErr error
	for attempt := range 5 {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		if err := losetupDetach(dev); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("after 5 attempts: %w", lastErr)
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
// the device after a VMM process exits.
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
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}
