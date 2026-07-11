package sandbox

import (
	"testing"

	"git.omukk.dev/wrenn/wrenn/pkg/envdclient"
)

func TestIsBusy(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		act  envdclient.Activity
		want bool
	}{
		// Default thresholds (zero cfg → defaults: cpu 5%, net 16K, disk 32K).
		{"idle", Config{}, envdclient.Activity{CPUUsedPct: 0.5, NetBps: 100, DiskBps: 200}, false},
		{"cpu just below", Config{}, envdclient.Activity{CPUUsedPct: 4.99}, false},
		{"cpu at threshold", Config{}, envdclient.Activity{CPUUsedPct: 5.0}, true},
		{"cpu above", Config{}, envdclient.Activity{CPUUsedPct: 80.0}, true},
		{"net just below", Config{}, envdclient.Activity{NetBps: 16*1024 - 1}, false},
		{"net at floor", Config{}, envdclient.Activity{NetBps: 16 * 1024}, true},
		{"disk just below", Config{}, envdclient.Activity{DiskBps: 32*1024 - 1}, false},
		{"disk at floor", Config{}, envdclient.Activity{DiskBps: 32 * 1024}, true},
		{"download: low cpu, high net", Config{}, envdclient.Activity{CPUUsedPct: 1.0, NetBps: 5 * 1024 * 1024}, true},

		// Explicit overrides take precedence over defaults.
		{
			"custom cpu threshold met",
			Config{CPUBusyPct: 20.0},
			envdclient.Activity{CPUUsedPct: 25.0},
			true,
		},
		{
			"custom cpu threshold not met",
			Config{CPUBusyPct: 20.0},
			envdclient.Activity{CPUUsedPct: 10.0},
			false,
		},
		{
			"custom net floor not met",
			Config{NetFloorBps: 1024 * 1024},
			envdclient.Activity{NetBps: 16 * 1024},
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Manager{cfg: tt.cfg}
			if got := m.isBusy(&tt.act); got != tt.want {
				t.Errorf("isBusy(%+v) = %v, want %v", tt.act, got, tt.want)
			}
		})
	}
}

func TestApplyBusySample(t *testing.T) {
	// Debounce requires busyDebounceSamples consecutive busy samples before the
	// first bump. Verify the streak math and bump timing.
	if busyDebounceSamples != 2 {
		t.Skip("test written for busyDebounceSamples=2")
	}

	tests := []struct {
		name        string
		startStreak int
		busy        bool
		wantStreak  int
		wantBump    bool
	}{
		{"first busy, no bump yet", 0, true, 1, false},
		{"second consecutive busy, bump", 1, true, 2, true},
		{"sustained busy keeps bumping, streak held", 2, true, 2, true},
		{"single noise spike from idle, no bump", 0, false, 0, false},
		{"idle resets a building streak", 1, false, 0, false},
		{"idle resets a saturated streak", 2, false, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStreak, gotBump := applyBusySample(tt.startStreak, tt.busy)
			if gotStreak != tt.wantStreak || gotBump != tt.wantBump {
				t.Errorf("applyBusySample(%d, %v) = (%d, %v), want (%d, %v)",
					tt.startStreak, tt.busy, gotStreak, gotBump, tt.wantStreak, tt.wantBump)
			}
		})
	}
}

// TestApplyBusySample_NoiseScenario walks a realistic sample sequence: brief
// noise never crosses the debounce, but sustained work does and then a return
// to idle resets — proving an isolated spike cannot keep a sandbox alive.
func TestApplyBusySample_NoiseScenario(t *testing.T) {
	if busyDebounceSamples != 2 {
		t.Skip("test written for busyDebounceSamples=2")
	}

	samples := []bool{true, false, false, true, true, true, false}
	wantBumps := []bool{false, false, false, false, true, true, false}

	streak := 0
	for i, busy := range samples {
		var bump bool
		streak, bump = applyBusySample(streak, busy)
		if bump != wantBumps[i] {
			t.Errorf("sample %d (busy=%v): bump = %v, want %v (streak=%d)",
				i, busy, bump, wantBumps[i], streak)
		}
	}
}
