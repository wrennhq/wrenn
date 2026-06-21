package validate

import (
	"fmt"
	"strings"
	"testing"
)

func TestMetadata(t *testing.T) {
	tests := []struct {
		name    string
		input   map[string]string
		wantErr bool
	}{
		{"nil", nil, false},
		{"empty", map[string]string{}, false},
		{"simple", map[string]string{"env": "prod", "owner": "alice"}, false},
		{"dotted-key", map[string]string{"team.name": "infra"}, false},
		{"empty-value", map[string]string{"flag": ""}, false},
		{"max-keys", makeKeys(MaxMetadataKeys), false},

		{"reserved-kernel", map[string]string{"kernel_version": "6.1.0"}, true},
		{"reserved-vmm", map[string]string{"vmm_version": "x"}, true},
		{"reserved-agent", map[string]string{"agent_version": "x"}, true},
		{"reserved-envd", map[string]string{"envd_version": "x"}, true},
		{"too-many-keys", makeKeys(MaxMetadataKeys + 1), true},
		{"bad-key-leading-dot", map[string]string{".hidden": "v"}, true},
		{"bad-key-space", map[string]string{"my key": "v"}, true},
		{"key-too-long", map[string]string{strings.Repeat("a", 65): "v"}, true},
		{"value-too-long", map[string]string{"k": strings.Repeat("a", MaxMetadataValueLen+1)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Metadata(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("Metadata(%v) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func makeKeys(n int) map[string]string {
	m := make(map[string]string, n)
	for i := range n {
		m[fmt.Sprintf("key%d", i)] = "v"
	}
	return m
}
