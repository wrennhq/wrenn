package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureVolumeFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "team", "vol", "data.img")
	const size = 5 * 1024 * 1024 // 5 MiB

	// First call creates the parent dirs and a sparse file of the right size.
	if err := ensureVolumeFile(path, size); err != nil {
		t.Fatalf("first ensureVolumeFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after create: %v", err)
	}
	if info.Size() != size {
		t.Fatalf("size = %d, want %d", info.Size(), size)
	}

	// Write data, then call again: an existing file must be left untouched so
	// previously-written data survives detach/re-attach.
	marker := []byte("volume-data-must-survive")
	if err := os.WriteFile(path, marker, 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := ensureVolumeFile(path, size); err != nil {
		t.Fatalf("second ensureVolumeFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(marker) {
		t.Fatalf("existing data was clobbered: got %q, want %q", got, marker)
	}
}
