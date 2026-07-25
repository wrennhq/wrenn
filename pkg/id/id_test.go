package id

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestBase36RoundTrip(t *testing.T) {
	for i := 0; i < 1000; i++ {
		orig := uuid.New()
		encoded := UUIDToBase36(orig)

		if len(encoded) != base36IDLen {
			t.Fatalf("expected %d chars, got %d: %s", base36IDLen, len(encoded), encoded)
		}

		decoded, err := base36ToUUID(encoded)
		if err != nil {
			t.Fatalf("decode failed: %v", err)
		}

		if decoded != orig {
			t.Fatalf("round-trip failed: %v → %s → %v", orig, encoded, decoded)
		}
	}
}

func TestBase36ZeroUUID(t *testing.T) {
	var zero [16]byte
	encoded := UUIDToBase36(zero)
	if encoded != "0000000000000000000000000" {
		t.Fatalf("zero UUID should encode to all zeros, got %s", encoded)
	}
	decoded, err := base36ToUUID(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if decoded != zero {
		t.Fatalf("round-trip failed for zero UUID")
	}
}

func TestFormatParseRoundTrip(t *testing.T) {
	id := NewSandboxID()
	formatted := FormatSandboxID(id)

	if formatted[:3] != "cl-" {
		t.Fatalf("expected cl- prefix, got %s", formatted)
	}
	if len(formatted) != 3+base36IDLen {
		t.Fatalf("expected %d chars total, got %d: %s", 3+base36IDLen, len(formatted), formatted)
	}

	parsed, err := ParseSandboxID(formatted)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed != id {
		t.Fatalf("round-trip failed: %v → %s → %v", id, formatted, parsed)
	}
}

func TestBase36InvalidInput(t *testing.T) {
	// Wrong length.
	if _, err := base36ToUUID("abc"); err == nil {
		t.Fatal("expected error for short input")
	}
	// Invalid character.
	if _, err := base36ToUUID("000000000000000000000000!"); err == nil {
		t.Fatal("expected error for invalid character")
	}
}

func TestPlatformTeamIDFormats(t *testing.T) {
	formatted := FormatTeamID(PlatformTeamID)
	parsed, err := ParseTeamID(formatted)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed != PlatformTeamID {
		t.Fatalf("platform team ID round-trip failed")
	}
}

func TestMaxUUID(t *testing.T) {
	max := [16]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	encoded := UUIDToBase36(max)
	if len(encoded) != base36IDLen {
		t.Fatalf("max UUID encoding wrong length: %d", len(encoded))
	}
	decoded, err := base36ToUUID(encoded)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if decoded != max {
		t.Fatalf("round-trip failed for max UUID")
	}
}

func TestVolumeIDRoundTrip(t *testing.T) {
	id := NewVolumeID()
	formatted := FormatVolumeID(id)
	if formatted[:len(PrefixVolume)] != PrefixVolume {
		t.Fatalf("expected %s prefix, got %s", PrefixVolume, formatted)
	}
	parsed, err := ParseVolumeID(formatted)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed != id {
		t.Fatalf("round-trip failed: %v → %s → %v", id, formatted, parsed)
	}
}

func TestVolumeSerial(t *testing.T) {
	// The virtio-blk serial (VIRTIO_BLK_ID_BYTES) caps at 20 bytes; the guest
	// resolves the device by this value, so it must always fit.
	for i := 0; i < 1000; i++ {
		s := VolumeSerial(NewVolumeID())
		if len(s) > 20 {
			t.Fatalf("serial %q exceeds 20 bytes (%d)", s, len(s))
		}
		if s == "" {
			t.Fatal("serial must not be empty")
		}
	}
	// Deterministic: the same volume yields the same serial (required so a
	// resumed capsule rebuilds the identical disk symlink).
	id := NewVolumeID()
	first := VolumeSerial(id)
	second := VolumeSerial(id)
	if first != second {
		t.Fatalf("VolumeSerial must be deterministic: %q != %q", first, second)
	}
}

func TestDefaultVolumeMountPath(t *testing.T) {
	id := NewVolumeID()
	want := "/mnt/" + FormatVolumeID(id)
	if got := DefaultVolumeMountPath(id); got != want {
		t.Fatalf("DefaultVolumeMountPath = %q, want %q", got, want)
	}
}

func BenchmarkFormatSandboxID(b *testing.B) {
	id := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FormatSandboxID(id)
	}
}

func BenchmarkParseSandboxID(b *testing.B) {
	id := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	s := FormatSandboxID(id)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ParseSandboxID(s)
	}
}
