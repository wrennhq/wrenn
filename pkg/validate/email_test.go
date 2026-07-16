package validate

import "testing"

func TestEmail(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"simple", "user@example.com", false},
		{"subdomain", "user@mail.example.com", false},
		{"plus-tag", "user+tag@example.com", false},
		{"dots", "first.last@example.co.uk", false},
		{"digits", "user123@ex4mple.io", false},

		{"empty", "", true},
		{"no-at", "userexample.com", true},
		{"no-domain", "user@", true},
		{"no-tld", "user@example", true},
		{"space", "user @example.com", true},
		{"angle-bracket", "user@<script>.com", true},
		{"xss-payload", "a@b.com<img src=x onerror=alert(1)>", true},
		{"quote", "us\"er@example.com", true},
		{"leading-space", " user@example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Email(tt.input); (err != nil) != tt.wantErr {
				t.Errorf("Email(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
