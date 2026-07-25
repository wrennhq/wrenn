package validate

import (
	"strings"
	"testing"
)

func TestVolumeName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"bare-name-gets-prefix", "cache", "vl-cache", false},
		{"already-prefixed", "vl-cache", "vl-cache", false},
		{"multi-group", "vl-build-cache-2", "vl-build-cache-2", false},
		{"digits", "vl-1", "vl-1", false},
		{"uppercase-lowered", "VL-Cache", "vl-cache", false},
		{"trims-space", "  cache  ", "vl-cache", false},
		{"default-id-form", "vl-" + strings.Repeat("a", 25), "vl-" + strings.Repeat("a", 25), false},
		{"max-length", "vl-" + strings.Repeat("a", 37), "vl-" + strings.Repeat("a", 37), false},
		// A second "vl-" is part of the name, not a repeated prefix — only one
		// is ever stripped, so this stays distinct from "vl-cache".
		{"double-prefix-kept", "vl-vl-cache", "vl-vl-cache", false},

		{"empty", "", "", true},
		{"prefix-only", "vl-", "", true},
		{"whitespace-only", "   ", "", true},
		{"too-long", "vl-" + strings.Repeat("a", 38), "", true},
		{"underscore", "my_cache", "", true},
		{"dot", "my.cache", "", true},
		{"slash", "team/cache", "", true},
		{"space-inside", "my cache", "", true},
		{"leading-dash", "-cache", "", true},
		{"trailing-dash", "cache-", "", true},
		{"double-dash", "my--cache", "", true},
		{"non-ascii", "café", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := VolumeName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("VolumeName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("VolumeName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Normalizing an already-normalized name must be a no-op, otherwise a name
// would drift every time it round-trips through the API.
func TestVolumeNameIsIdempotent(t *testing.T) {
	for _, input := range []string{"cache", "vl-cache", "VL-Build-Cache", "vl-vl-cache"} {
		once, err := VolumeName(input)
		if err != nil {
			t.Fatalf("VolumeName(%q) unexpected error: %v", input, err)
		}
		twice, err := VolumeName(once)
		if err != nil {
			t.Fatalf("VolumeName(%q) unexpected error: %v", once, err)
		}
		if once != twice {
			t.Errorf("VolumeName not idempotent for %q: %q then %q", input, once, twice)
		}
	}
}

// A volume ID prefix must never be mistaken for a name prefix, which is what
// lets one path segment carry either without ambiguity.
func TestVolumeNamePrefixDoesNotCollideWithIDPrefix(t *testing.T) {
	if strings.HasPrefix("vol-", VolumeNamePrefix) || strings.HasPrefix(VolumeNamePrefix, "vol-") {
		t.Fatalf("volume name prefix %q collides with the vol- ID prefix", VolumeNamePrefix)
	}
}
