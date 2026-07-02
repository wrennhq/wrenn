package network

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestSlotAllocator_InMemory(t *testing.T) {
	a := NewSlotAllocator("")

	idx, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if idx != 1 {
		t.Fatalf("first slot = %d, want 1", idx)
	}

	if err := a.Reserve(idx); err == nil {
		t.Fatal("Reserve of allocated slot should fail")
	}

	a.Release(idx)
	if err := a.Reserve(idx); err != nil {
		t.Fatalf("Reserve after Release: %v", err)
	}
}

func TestSlotAllocator_ClaimFiles(t *testing.T) {
	dir := t.TempDir()
	a := NewSlotAllocator(dir)

	idx, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	claim := filepath.Join(dir, strconv.Itoa(idx))
	if _, err := os.Stat(claim); err != nil {
		t.Fatalf("claim file missing after Allocate: %v", err)
	}

	a.Release(idx)
	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Fatalf("claim file still present after Release (err=%v)", err)
	}
}

func TestSlotAllocator_AllocateSkipsExternalClaims(t *testing.T) {
	dir := t.TempDir()
	// Simulate another process holding slots 1 and 2.
	for _, n := range []string{"1", "2"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	a := NewSlotAllocator(dir)
	idx, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if idx != 3 {
		t.Fatalf("Allocate = %d, want 3 (slots 1-2 externally claimed)", idx)
	}
}

func TestSlotAllocator_SeedAndReserveAdopt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "5"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	a := NewSlotAllocator(dir)
	if err := a.SeedFromDir(); err != nil {
		t.Fatalf("SeedFromDir: %v", err)
	}

	// Seeded slots are never handed out by Allocate.
	idx, err := a.Allocate()
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}
	if idx == 5 {
		t.Fatal("Allocate handed out a seeded slot")
	}

	// A seeded slot can be adopted exactly once via Reserve (restore path)...
	if err := a.Reserve(5); err != nil {
		t.Fatalf("Reserve of seeded slot: %v", err)
	}
	// ...after which it is owned and a second Reserve fails.
	if err := a.Reserve(5); err == nil {
		t.Fatal("second Reserve of adopted slot should fail")
	}
}

func TestSlotAllocator_ReserveCreatesClaimFile(t *testing.T) {
	dir := t.TempDir()
	a := NewSlotAllocator(dir)

	if err := a.Reserve(7); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "7")); err != nil {
		t.Fatalf("claim file missing after Reserve: %v", err)
	}
}

func TestSlotAllocator_ReserveOutOfRange(t *testing.T) {
	a := NewSlotAllocator("")
	for _, idx := range []int{0, -1, 32768} {
		if err := a.Reserve(idx); err == nil {
			t.Fatalf("Reserve(%d) should fail", idx)
		}
	}
}

func TestSlotAllocator_SeedFromMissingDir(t *testing.T) {
	a := NewSlotAllocator(filepath.Join(t.TempDir(), "does-not-exist"))
	if err := a.SeedFromDir(); err != nil {
		t.Fatalf("SeedFromDir on missing dir: %v", err)
	}
}
