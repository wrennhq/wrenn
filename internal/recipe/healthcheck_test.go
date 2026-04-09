package recipe

import (
	"testing"
	"time"
)

func TestParseHealthcheck(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    HealthcheckConfig
		wantErr bool
	}{
		{
			name:  "plain command",
			input: "curl -f http://localhost:8080",
			want: HealthcheckConfig{
				Cmd:      "curl -f http://localhost:8080",
				Interval: 3 * time.Second,
				Timeout:  10 * time.Second,
			},
			wantErr: false,
		},
		{
			name:  "all flags",
			input: "--interval=5s --timeout=2s --start-period=15s --retries=3 ping -c 1 8.8.8.8",
			want: HealthcheckConfig{
				Cmd:         "ping -c 1 8.8.8.8",
				Interval:    5 * time.Second,
				Timeout:     2 * time.Second,
				StartPeriod: 15 * time.Second,
				Retries:     3,
			},
			wantErr: false,
		},
		{
			name:  "partial flags",
			input: "--timeout=5s my-custom-check --verbose",
			want: HealthcheckConfig{
				Cmd:      "my-custom-check --verbose",
				Interval: 3 * time.Second,
				Timeout:  5 * time.Second,
			},
			wantErr: false,
		},
		{
			name:  "retries only",
			input: "--retries=5 test.sh",
			want: HealthcheckConfig{
				Cmd:      "test.sh",
				Interval: 3 * time.Second,
				Timeout:  10 * time.Second,
				Retries:  5,
			},
			wantErr: false,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			input:   "   \t  \n ",
			wantErr: true,
		},
		{
			name:    "flags but no command",
			input:   "--interval=5s --retries=2",
			wantErr: true,
		},
		{
			name:    "unknown flag",
			input:   "--magic=true my-check",
			wantErr: true,
		},
		{
			name:    "invalid duration",
			input:   "--interval=5smiles check.sh",
			wantErr: true,
		},
		{
			name:    "invalid retries",
			input:   "--retries=five check.sh",
			wantErr: true,
		},
		{
			name:  "command with dashes",
			input: "--interval=2s command-with-dash --flag=value",
			want: HealthcheckConfig{
				Cmd:      "command-with-dash --flag=value",
				Interval: 2 * time.Second,
				Timeout:  10 * time.Second,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseHealthcheck(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseHealthcheck() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if got.Cmd != tt.want.Cmd {
					t.Errorf("Cmd got = %v, want %v", got.Cmd, tt.want.Cmd)
				}
				if got.Interval != tt.want.Interval {
					t.Errorf("Interval got = %v, want %v", got.Interval, tt.want.Interval)
				}
				if got.Timeout != tt.want.Timeout {
					t.Errorf("Timeout got = %v, want %v", got.Timeout, tt.want.Timeout)
				}
				if got.StartPeriod != tt.want.StartPeriod {
					t.Errorf("StartPeriod got = %v, want %v", got.StartPeriod, tt.want.StartPeriod)
				}
				if got.Retries != tt.want.Retries {
					t.Errorf("Retries got = %v, want %v", got.Retries, tt.want.Retries)
				}
			}
		})
	}
}
