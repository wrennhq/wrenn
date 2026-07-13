package validate

import "testing"

func TestDisplayName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "John", false},
		{"with-space", "John Doe", false},
		{"with-dot", "John D.", false},
		{"with-dash", "Anne-Marie", false},
		{"with-underscore", "cool_name", false},
		{"numbers", "user123", false},
		{"max-length", repeat("a", 100), false},

		{"empty", "", true},
		{"too-long", repeat("a", 101), true},
		{"angle-open", "John<", true},
		{"angle-close", "John>", true},
		{"ampersand", "A&B", true},
		{"double-quote", "he\"llo", true},
		{"single-quote", "O'Brien", true},
		{"xss-img", "<img src=x onerror=alert(1)>", true},
		{"xss-script", "<script>alert(1)</script>", true},
		{"at-sign", "a@b", true},
		{"unicode-letter", "José", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := DisplayName(tt.input); (err != nil) != tt.wantErr {
				t.Errorf("DisplayName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestSanitizeDisplayName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		fallback string
		want     string
	}{
		{"clean", "John Doe", "user", "John Doe"},
		{"strip-html", "<img src=x>Bob", "user", "img srcxBob"},
		{"strip-angles-only", "a<b>c", "user", "abc"},
		{"strip-unicode", "José", "user", "Jos"},
		{"all-stripped", "<>&\"'", "user", "user"},
		{"empty", "", "fallback", "fallback"},
		{"trims", "  spaced  ", "user", "spaced"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeDisplayName(tt.input, tt.fallback); got != tt.want {
				t.Errorf("SanitizeDisplayName(%q, %q) = %q, want %q", tt.input, tt.fallback, got, tt.want)
			}
			// A sanitized non-fallback result must itself be a valid DisplayName.
			if got := SanitizeDisplayName(tt.input, tt.fallback); got != tt.fallback {
				if err := DisplayName(got); err != nil {
					t.Errorf("SanitizeDisplayName(%q) = %q is not a valid DisplayName: %v", tt.input, got, err)
				}
			}
		})
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}
