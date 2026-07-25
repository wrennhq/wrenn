package units

import "testing"

func TestParseSizeToMB(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr bool
	}{
		{"gibibytes", "5G", 5120, false},
		{"gibibytes-explicit", "2Gi", 2048, false},
		{"megabytes", "1000M", 1000, false},
		{"mebibytes", "512Mi", 512, false},
		{"bare-number-is-mb", "2048", 2048, false},
		{"fractional", "1.5G", 1536, false},
		{"trims-space", "  20Gi  ", 20480, false},
		{"default-volume-cap", "20Gi", 20 * 1024, false},

		{"empty", "", 0, true},
		{"whitespace-only", "   ", 0, true},
		{"no-number", "Gi", 0, true},
		{"negative", "-5G", 0, true},
		{"unknown-suffix", "5T", 0, true},
		{"unknown-suffix-bytes", "500B", 0, true},

		// Sizes are int32 megabytes everywhere downstream. Without an explicit
		// bound these truncate: 4194305Gi wraps to exactly 1024 MB, which would
		// sail past both the minimum and maximum checks as a 1 GiB volume.
		{"overflows-int32", "4194305Gi", 0, true},
		{"overflows-int32-exactly", "4194304Gi", 0, true},
		{"huge", "999999999Gi", 0, true},
		{"largest-accepted", "2147483647", 1<<31 - 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseSizeToMB(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseSizeToMB(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseSizeToMB(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatMB(t *testing.T) {
	tests := []struct {
		input int
		want  string
	}{
		{20 * 1024, "20Gi"},
		{1024, "1Gi"},
		{5120, "5Gi"},
		{100, "100Mi"},
		{1500, "1500Mi"},
		{0, "0Mi"},
	}
	for _, tt := range tests {
		if got := FormatMB(tt.input); got != tt.want {
			t.Errorf("FormatMB(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// FormatMB's output must feed back into ParseSizeToMB unchanged, so an error
// message quoting a limit can be pasted straight into a request or env var.
func TestFormatMBRoundTrips(t *testing.T) {
	for _, mb := range []int{100, 1024, 5120, 20 * 1024, 1500} {
		got, err := ParseSizeToMB(FormatMB(mb))
		if err != nil {
			t.Fatalf("ParseSizeToMB(FormatMB(%d)) unexpected error: %v", mb, err)
		}
		if got != mb {
			t.Errorf("round trip of %d MB produced %d", mb, got)
		}
	}
}
