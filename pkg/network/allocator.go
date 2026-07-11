package network

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// SlotAllocator manages network slot indices for sandboxes.
// Each sandbox needs a unique slot index for its network addressing.
//
// When slotsDir is set, every claim is additionally recorded as a file
// {slotsDir}/{index} created with O_CREAT|O_EXCL, which is atomic on local
// filesystems. That makes allocation safe across processes: two concurrent
// short-lived CLI invocations (each with its own in-memory allocator) can
// never claim the same slot, which would otherwise silently give two VMs the
// same host-reachable IP.
type SlotAllocator struct {
	mu    sync.Mutex
	inUse map[int]bool
	// seeded marks slots that have a claim file on disk but no owner in this
	// process yet (populated by SeedFromDir). Allocate never hands them out;
	// Reserve may adopt one — the caller restoring a paused or running
	// sandbox owns the on-disk state that justifies the claim.
	seeded   map[int]bool
	slotsDir string // empty → in-memory only (single-process mode)
}

// NewSlotAllocator creates a new slot allocator. slotsDir enables
// cross-process claim files; pass "" for in-memory-only allocation.
func NewSlotAllocator(slotsDir string) *SlotAllocator {
	return &SlotAllocator{
		inUse:    make(map[int]bool),
		seeded:   make(map[int]bool),
		slotsDir: slotsDir,
	}
}

// Allocate returns the next available slot index (1-based).
func (a *SlotAllocator) Allocate() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := 1; i <= 32767; i++ {
		if a.inUse[i] {
			continue
		}
		if err := a.claimFile(i); err != nil {
			if os.IsExist(err) {
				// Another process holds this slot; remember and move on.
				a.inUse[i] = true
				continue
			}
			return 0, fmt.Errorf("claim slot %d: %w", i, err)
		}
		a.inUse[i] = true
		return i, nil
	}
	return 0, fmt.Errorf("no free network slots")
}

// Release frees a slot index for reuse.
func (a *SlotAllocator) Release(index int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.inUse, index)
	delete(a.seeded, index)
	if a.slotsDir != "" {
		_ = os.Remove(a.slotFile(index))
	}
}

// Reserve marks a specific slot index as in use. Returns an error if the
// index is out of range or already taken in this process. Used on resume and
// re-attach to re-acquire the slot a sandbox previously held so its
// host-reachable IP stays stable. The claim file may legitimately already
// exist (written by the process that created the sandbox), so EEXIST is not
// an error here — on-disk sandbox state is what proves ownership.
func (a *SlotAllocator) Reserve(index int) error {
	if index < 1 || index > 32767 {
		return fmt.Errorf("slot index out of range: %d", index)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inUse[index] && !a.seeded[index] {
		return fmt.Errorf("slot %d already in use", index)
	}
	if err := a.claimFile(index); err != nil && !os.IsExist(err) {
		return fmt.Errorf("claim slot %d: %w", index, err)
	}
	a.inUse[index] = true
	delete(a.seeded, index)
	return nil
}

// SeedFromDir marks every slot that has a claim file as in-use in this
// process. Called once at startup (before any Allocate) so slots held by
// concurrently running processes — including creates that have not yet
// persisted their sandbox state — are never handed out.
func (a *SlotAllocator) SeedFromDir() error {
	if a.slotsDir == "" {
		return nil
	}
	entries, err := os.ReadDir(a.slotsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read slots dir: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range entries {
		if idx, err := strconv.Atoi(e.Name()); err == nil && idx >= 1 && idx <= 32767 {
			a.inUse[idx] = true
			a.seeded[idx] = true
		}
	}
	return nil
}

// claimFile atomically creates the claim file for a slot. Returns an error
// satisfying os.IsExist when another process already holds the slot. No-op
// (nil) when slotsDir is unset.
func (a *SlotAllocator) claimFile(index int) error {
	if a.slotsDir == "" {
		return nil
	}
	if err := os.MkdirAll(a.slotsDir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(a.slotFile(index), os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

func (a *SlotAllocator) slotFile(index int) string {
	return filepath.Join(a.slotsDir, strconv.Itoa(index))
}
