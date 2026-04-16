package network

import (
	"fmt"
	"sync"
)

// SlotAllocator manages network slot indices for sandboxes.
// Each sandbox needs a unique slot index for its network addressing.
type SlotAllocator struct {
	mu    sync.Mutex
	inUse map[int]bool
}

// NewSlotAllocator creates a new slot allocator.
func NewSlotAllocator() *SlotAllocator {
	return &SlotAllocator{
		inUse: make(map[int]bool),
	}
}

// Allocate returns the next available slot index (1-based).
func (a *SlotAllocator) Allocate() (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := 1; i <= 32767; i++ {
		if !a.inUse[i] {
			a.inUse[i] = true
			return i, nil
		}
	}
	return 0, fmt.Errorf("no free network slots")
}

// Release frees a slot index for reuse.
func (a *SlotAllocator) Release(index int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.inUse, index)
}
