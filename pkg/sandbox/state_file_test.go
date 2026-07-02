package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.omukk.dev/wrenn/wrenn/pkg/layout"
)

func writeTestRunningState(t *testing.T, wrennDir string, st *runningState) {
	t.Helper()
	dir := layout.SandboxDir(wrennDir, st.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, runningStateFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunningStateRoundtrip(t *testing.T) {
	wrennDir := t.TempDir()
	want := &runningState{
		Version:            runningStateVersion,
		ID:                 "sb-cafe0123",
		TeamID:             "00000000-0000-0000-0000-000000000000",
		TemplateID:         "00000000-0000-0000-0000-000000000001",
		VCPUs:              2,
		MemoryMB:           1024,
		TimeoutSec:         300,
		SlotIndex:          7,
		BaseImagePath:      "/var/lib/wrenn/images/teams/x/y/rootfs.ext4",
		CowPath:            "/var/lib/wrenn/sandboxes/sb-cafe0123/rootfs.cow",
		DMName:             "wrenn-sb-cafe0123",
		SandboxDir:         "/tmp/ch-vm-sb-cafe0123",
		CHPID:              4242,
		CHSocket:           "/tmp/ch-sb-cafe0123.sock",
		SandboxDirOverride: "/tmp/ch-vm-sb-original",
		CreatedAt:          time.Now().Truncate(0),
		Metadata:           map[string]string{"kernel_version": "6.1.102"},
	}
	writeTestRunningState(t, wrennDir, want)

	got, err := readRunningState(wrennDir, want.ID)
	if err != nil {
		t.Fatalf("readRunningState: %v", err)
	}
	if got.ID != want.ID || got.SlotIndex != want.SlotIndex ||
		got.CHPID != want.CHPID || got.DMName != want.DMName ||
		got.SandboxDirOverride != want.SandboxDirOverride ||
		!got.CreatedAt.Equal(want.CreatedAt) ||
		got.Metadata["kernel_version"] != want.Metadata["kernel_version"] {
		t.Fatalf("roundtrip mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestRunningStateVersionMismatch(t *testing.T) {
	wrennDir := t.TempDir()
	st := &runningState{Version: runningStateVersion + 1, ID: "sb-deadbeef", SlotIndex: 1}
	writeTestRunningState(t, wrennDir, st)

	if _, err := readRunningState(wrennDir, st.ID); err == nil {
		t.Fatal("readRunningState should reject unknown version")
	}
}

func TestRunningStateIDMismatch(t *testing.T) {
	wrennDir := t.TempDir()
	st := &runningState{Version: runningStateVersion, ID: "sb-other", SlotIndex: 1}
	// Write the file under a directory whose name does not match st.ID.
	dir := layout.SandboxDir(wrennDir, "sb-dirname")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(dir, runningStateFile), data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := readRunningState(wrennDir, "sb-dirname"); err == nil {
		t.Fatal("readRunningState should reject id mismatch")
	}
}

func TestRunningStateMissingFile(t *testing.T) {
	if _, err := readRunningState(t.TempDir(), "sb-none"); !os.IsNotExist(err) {
		t.Fatalf("want os.IsNotExist error, got %v", err)
	}
}

func TestDeleteRunningState(t *testing.T) {
	wrennDir := t.TempDir()
	st := &runningState{Version: runningStateVersion, ID: "sb-gone", SlotIndex: 1}
	writeTestRunningState(t, wrennDir, st)

	deleteRunningState(wrennDir, st.ID)
	if _, err := readRunningState(wrennDir, st.ID); !os.IsNotExist(err) {
		t.Fatalf("state file should be gone, got %v", err)
	}
	// Idempotent.
	deleteRunningState(wrennDir, st.ID)
}
