package email

import (
	"context"
	"strings"
	"testing"
)

func TestNoopMailerDoesNotError(t *testing.T) {
	m := &noopMailer{}
	err := m.Send(context.Background(), "test@example.com", "Test Subject", EmailData{
		RecipientName: "Alice",
		Message:       "Hello world",
	})
	if err != nil {
		t.Fatalf("noopMailer.Send() returned error: %v", err)
	}
}

func TestNewReturnsNoopWhenHostEmpty(t *testing.T) {
	m := New(Config{})
	if _, ok := m.(*noopMailer); !ok {
		t.Fatalf("expected noopMailer, got %T", m)
	}
}

func TestNewReturnsMailerWhenHostSet(t *testing.T) {
	m := New(Config{Host: "smtp.example.com"})
	if _, ok := m.(*mailer); !ok {
		t.Fatalf("expected *mailer, got %T", m)
	}
}

func TestTemplateRenderHTML(t *testing.T) {
	tmpl := mustLoadTemplates()

	tests := []struct {
		name string
		data EmailData
		want []string // substrings that must appear in output
	}{
		{
			name: "with all fields",
			data: EmailData{
				RecipientName: "Alice",
				Message:       "Welcome to Wrenn!",
				Button:        &Button{Text: "Get Started", URL: "https://wrenn.dev"},
				Closing:       "See you soon.",
			},
			want: []string{"Alice", "Welcome to Wrenn!", "Get Started", "https://wrenn.dev", "See you soon."},
		},
		{
			name: "message only",
			data: EmailData{
				Message: "Your password has been changed.",
			},
			want: []string{"Your password has been changed."},
		},
		{
			name: "with button no closing",
			data: EmailData{
				RecipientName: "Bob",
				Message:       "Reset your password.",
				Button:        &Button{Text: "Reset Password", URL: "https://wrenn.dev/reset?token=abc"},
			},
			want: []string{"Bob", "Reset your password.", "Reset Password", "https://wrenn.dev/reset?token=abc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html, err := tmpl.renderHTML(tt.data)
			if err != nil {
				t.Fatalf("renderHTML() error: %v", err)
			}
			for _, s := range tt.want {
				if !strings.Contains(html, s) {
					t.Errorf("renderHTML() missing substring %q", s)
				}
			}
			// Verify basic HTML structure.
			if !strings.Contains(html, "<!DOCTYPE html>") {
				t.Error("renderHTML() missing DOCTYPE")
			}
			if !strings.Contains(html, "wrenn.dev") {
				t.Error("renderHTML() missing wrenn.dev reference")
			}
		})
	}
}

func TestTemplateRenderText(t *testing.T) {
	tmpl := mustLoadTemplates()

	tests := []struct {
		name string
		data EmailData
		want []string
	}{
		{
			name: "with all fields",
			data: EmailData{
				RecipientName: "Alice",
				Message:       "Welcome to Wrenn!",
				Button:        &Button{Text: "Get Started", URL: "https://wrenn.dev"},
				Closing:       "See you soon.",
			},
			want: []string{"Hello Alice", "Welcome to Wrenn!", "Get Started: https://wrenn.dev", "See you soon."},
		},
		{
			name: "message only",
			data: EmailData{
				Message: "Done.",
			},
			want: []string{"Hello,", "Done."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, err := tmpl.renderText(tt.data)
			if err != nil {
				t.Fatalf("renderText() error: %v", err)
			}
			for _, s := range tt.want {
				if !strings.Contains(text, s) {
					t.Errorf("renderText() missing substring %q\nGot:\n%s", s, text)
				}
			}
		})
	}
}

func TestBuildMIME(t *testing.T) {
	msg, err := buildMIME("noreply@wrenn.dev", "user@example.com", "Test Subject", "<h1>HTML</h1>", "Plain text")
	if err != nil {
		t.Fatalf("buildMIME() error: %v", err)
	}

	s := string(msg)
	if !strings.Contains(s, "From:") {
		t.Error("missing From header")
	}
	if !strings.Contains(s, "To: user@example.com") {
		t.Error("missing To header")
	}
	if !strings.Contains(s, "Wrenn") {
		t.Error("missing Wrenn sender name")
	}
	if !strings.Contains(s, "multipart/alternative") {
		t.Error("missing multipart/alternative content type")
	}
	if !strings.Contains(s, "text/plain") {
		t.Error("missing text/plain part")
	}
	if !strings.Contains(s, "text/html") {
		t.Error("missing text/html part")
	}
}

func TestBuildMIMENonASCII(t *testing.T) {
	msg, err := buildMIME("noreply@wrenn.dev", "user@example.com", "Test", "<p>\u00c5ngstr\u00f6m</p>", "Hello \u00c5ngstr\u00f6m")
	if err != nil {
		t.Fatalf("buildMIME() error: %v", err)
	}

	s := string(msg)
	// Non-ASCII characters should be QP-encoded, not appear as raw bytes.
	// \u00c5 (U+00C5, 0xC3 0x85 in UTF-8) should be encoded as =C3=85.
	if !strings.Contains(s, "=C3=85") {
		t.Error("non-ASCII character not quoted-printable encoded")
	}
}

func TestSanitizeHeader(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"normal@example.com", "normal@example.com"},
		{"injected\r\nBcc: evil@example.com", "injectedBcc: evil@example.com"},
		{"has\nnewline", "hasnewline"},
		{"has\rcarriage", "hascarriage"},
	}
	for _, tt := range tests {
		got := sanitizeHeader(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeHeader(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
