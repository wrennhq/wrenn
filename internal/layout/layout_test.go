package layout

import (
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/id"
)

func TestIsMinimal(t *testing.T) {
	tests := []struct {
		name       string
		teamID     pgtype.UUID
		templateID pgtype.UUID
		want       bool
	}{
		{
			name:       "both zeros",
			teamID:     id.PlatformTeamID,
			templateID: id.MinimalTemplateID,
			want:       true,
		},
		{
			name:       "non-zero team",
			teamID:     pgtype.UUID{Bytes: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, Valid: true},
			templateID: id.MinimalTemplateID,
			want:       false,
		},
		{
			name:       "non-zero template",
			teamID:     id.PlatformTeamID,
			templateID: pgtype.UUID{Bytes: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, Valid: true},
			want:       false,
		},
		{
			name:       "both non-zero",
			teamID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
			templateID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsMinimal(tt.teamID, tt.templateID); got != tt.want {
				t.Errorf("IsMinimal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTemplateDir(t *testing.T) {
	wrennDir := "/var/lib/wrenn"

	t.Run("minimal", func(t *testing.T) {
		got := TemplateDir(wrennDir, id.PlatformTeamID, id.MinimalTemplateID)
		want := filepath.Join(wrennDir, "images", "minimal")
		if got != want {
			t.Errorf("TemplateDir() = %q, want %q", got, want)
		}
	})

	t.Run("team template", func(t *testing.T) {
		teamID := pgtype.UUID{Bytes: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, Valid: true}
		tmplID := pgtype.UUID{Bytes: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}, Valid: true}
		got := TemplateDir(wrennDir, teamID, tmplID)
		want := filepath.Join(wrennDir, "images", "teams",
			id.UUIDToBase36(teamID.Bytes),
			id.UUIDToBase36(tmplID.Bytes))
		if got != want {
			t.Errorf("TemplateDir() = %q, want %q", got, want)
		}
	})

	t.Run("global template (platform team, non-zero template)", func(t *testing.T) {
		tmplID := pgtype.UUID{Bytes: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 5}, Valid: true}
		got := TemplateDir(wrennDir, id.PlatformTeamID, tmplID)
		want := filepath.Join(wrennDir, "images", "teams",
			id.UUIDToBase36(id.PlatformTeamID.Bytes),
			id.UUIDToBase36(tmplID.Bytes))
		if got != want {
			t.Errorf("TemplateDir() = %q, want %q", got, want)
		}
	})
}

func TestTemplateRootfs(t *testing.T) {
	wrennDir := "/var/lib/wrenn"
	got := TemplateRootfs(wrennDir, id.PlatformTeamID, id.MinimalTemplateID)
	want := filepath.Join(wrennDir, "images", "minimal", "rootfs.ext4")
	if got != want {
		t.Errorf("TemplateRootfs() = %q, want %q", got, want)
	}
}

func TestPauseSnapshotDir(t *testing.T) {
	got := PauseSnapshotDir("/var/lib/wrenn", "sb-abc123")
	want := "/var/lib/wrenn/snapshots/sb-abc123"
	if got != want {
		t.Errorf("PauseSnapshotDir() = %q, want %q", got, want)
	}
}

func TestSandboxesDir(t *testing.T) {
	got := SandboxesDir("/var/lib/wrenn")
	want := "/var/lib/wrenn/sandboxes"
	if got != want {
		t.Errorf("SandboxesDir() = %q, want %q", got, want)
	}
}

func TestKernelPath(t *testing.T) {
	got := KernelPath("/var/lib/wrenn")
	want := "/var/lib/wrenn/kernels/vmlinux"
	if got != want {
		t.Errorf("KernelPath() = %q, want %q", got, want)
	}
}
