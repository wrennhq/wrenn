package recipe

import (
	"reflect"
	"testing"
	"time"
)

func TestParseStep(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Step
		wantErr bool
	}{
		// RUN
		{
			name:  "RUN basic",
			input: "RUN apt install -y curl",
			want:  Step{Kind: KindRUN, Raw: "RUN apt install -y curl", Shell: "apt install -y curl"},
		},
		{
			name:  "RUN lowercase",
			input: "run echo hello",
			want:  Step{Kind: KindRUN, Raw: "run echo hello", Shell: "echo hello"},
		},
		{
			name:  "RUN with timeout",
			input: "RUN --timeout=5m npm install",
			want:  Step{Kind: KindRUN, Raw: "RUN --timeout=5m npm install", Shell: "npm install", Timeout: 5 * time.Minute},
		},
		{
			name:  "RUN with timeout seconds",
			input: "RUN --timeout=30s make build",
			want:  Step{Kind: KindRUN, Raw: "RUN --timeout=30s make build", Shell: "make build", Timeout: 30 * time.Second},
		},
		{
			name:    "RUN no command",
			input:   "RUN",
			wantErr: true,
		},
		{
			name:    "RUN timeout no command",
			input:   "RUN --timeout=5m",
			wantErr: true,
		},
		{
			name:    "RUN invalid timeout",
			input:   "RUN --timeout=notaduration echo hi",
			wantErr: true,
		},
		// START
		{
			name:  "START basic",
			input: "START python3 app.py",
			want:  Step{Kind: KindSTART, Raw: "START python3 app.py", Shell: "python3 app.py"},
		},
		{
			name:  "START uppercase",
			input: "START node server.js --port=8080",
			want:  Step{Kind: KindSTART, Raw: "START node server.js --port=8080", Shell: "node server.js --port=8080"},
		},
		{
			name:    "START no command",
			input:   "START",
			wantErr: true,
		},
		// ENV
		{
			name:  "ENV basic",
			input: "ENV FOO=bar",
			want:  Step{Kind: KindENV, Raw: "ENV FOO=bar", Key: "FOO", Value: "bar"},
		},
		{
			name:  "ENV value with spaces",
			input: "ENV GREETING=hello world",
			want:  Step{Kind: KindENV, Raw: "ENV GREETING=hello world", Key: "GREETING", Value: "hello world"},
		},
		{
			name:  "ENV value with equals sign",
			input: "ENV URL=http://example.com?a=1",
			want:  Step{Kind: KindENV, Raw: "ENV URL=http://example.com?a=1", Key: "URL", Value: "http://example.com?a=1"},
		},
		{
			name:  "ENV empty value",
			input: "ENV FOO=",
			want:  Step{Kind: KindENV, Raw: "ENV FOO=", Key: "FOO", Value: ""},
		},
		{
			name:    "ENV missing equals",
			input:   "ENV FOO",
			wantErr: true,
		},
		{
			name:    "ENV empty key",
			input:   "ENV =value",
			wantErr: true,
		},
		// WORKDIR
		{
			name:  "WORKDIR basic",
			input: "WORKDIR /app",
			want:  Step{Kind: KindWORKDIR, Raw: "WORKDIR /app", Path: "/app"},
		},
		{
			name:  "WORKDIR with spaces in path",
			input: "WORKDIR /my project",
			want:  Step{Kind: KindWORKDIR, Raw: "WORKDIR /my project", Path: "/my project"},
		},
		{
			name:    "WORKDIR empty",
			input:   "WORKDIR",
			wantErr: true,
		},
		// USER
		{
			name:  "USER basic",
			input: "USER www-data",
			want:  Step{Kind: KindUSER, Raw: "USER www-data", Key: "www-data"},
		},
		{
			name:    "USER empty",
			input:   "USER",
			wantErr: true,
		},
		{
			name:    "USER invalid chars",
			input:   "USER bad user",
			wantErr: true,
		},
		// COPY
		{
			name:  "COPY basic",
			input: "COPY config.yaml /etc/app/config.yaml",
			want:  Step{Kind: KindCOPY, Raw: "COPY config.yaml /etc/app/config.yaml", Srcs: []string{"config.yaml"}, Dst: "/etc/app/config.yaml"},
		},
		{
			name:  "COPY multiple sources",
			input: "COPY a.txt b.txt /dest/",
			want:  Step{Kind: KindCOPY, Raw: "COPY a.txt b.txt /dest/", Srcs: []string{"a.txt", "b.txt"}, Dst: "/dest/"},
		},
		{
			name:    "COPY missing dst",
			input:   "COPY config.yaml",
			wantErr: true,
		},
		{
			name:    "COPY empty",
			input:   "COPY",
			wantErr: true,
		},
		// Unknown keyword
		{
			name:    "unknown keyword",
			input:   "FROBNICATE something",
			wantErr: true,
		},
		// Empty input
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseStep(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseStep(%q) expected error, got %+v", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseStep(%q) unexpected error: %v", tc.input, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseStep(%q)\n  got  %+v\n  want %+v", tc.input, got, tc.want)
			}
		})
	}
}

func TestParseRecipe(t *testing.T) {
	t.Run("valid recipe", func(t *testing.T) {
		lines := []string{
			"RUN apt update",
			"WORKDIR /app",
			"ENV PORT=8080",
			"START python3 server.py",
			"RUN --timeout=2m pip install -r requirements.txt",
		}
		steps, err := ParseRecipe(lines)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(steps) != 5 {
			t.Fatalf("expected 5 steps, got %d", len(steps))
		}
		if steps[0].Kind != KindRUN {
			t.Errorf("step 0: want KindRUN, got %v", steps[0].Kind)
		}
		if steps[1].Kind != KindWORKDIR {
			t.Errorf("step 1: want KindWORKDIR, got %v", steps[1].Kind)
		}
		if steps[3].Kind != KindSTART {
			t.Errorf("step 3: want KindSTART, got %v", steps[3].Kind)
		}
		if steps[4].Timeout != 2*time.Minute {
			t.Errorf("step 4: want 2m timeout, got %v", steps[4].Timeout)
		}
	})

	t.Run("error on invalid line", func(t *testing.T) {
		lines := []string{
			"RUN apt update",
			"BADCMD something",
		}
		_, err := ParseRecipe(lines)
		if err == nil {
			t.Fatal("expected error for invalid line, got nil")
		}
	})

	t.Run("empty recipe", func(t *testing.T) {
		steps, err := ParseRecipe(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(steps) != 0 {
			t.Fatalf("expected 0 steps, got %d", len(steps))
		}
	})
}
