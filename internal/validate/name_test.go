package validate

import "testing"

func TestSafeName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "minimal", false},
		{"with-dash", "template-abc123", false},
		{"with-dot", "my-snapshot.v2", false},
		{"sandbox-id", "sb-12345678", false},
		{"single-char", "a", false},
		{"numbers", "123", false},
		{"max-length", "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz01", false},

		{"empty", "", true},
		{"dot-dot", "..", true},
		{"single-dot", ".", true},
		{"leading-dot", ".hidden", true},
		{"slash", "foo/bar", true},
		{"backslash", "foo\\bar", true},
		{"traversal", "../etc/passwd", true},
		{"embedded-traversal", "foo/../bar", true},
		{"space", "foo bar", true},
		{"too-long", "abcdefghijklmnopqrstuvwxyz012345678901abcdefghijklmnopqrstuvwxyz01", true},
		{"absolute", "/etc/passwd", true},
		{"tilde", "~root", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SafeName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("SafeName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
