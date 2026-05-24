package layout

import (
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

func TestIsSystemTemplate(t *testing.T) {
	tests := []struct {
		name       string
		teamID     pgtype.UUID
		templateID pgtype.UUID
		want       bool
	}{
		{
			name:       "ubuntu (zeros, zeros)",
			teamID:     id.PlatformTeamID,
			templateID: id.UbuntuTemplateID,
			want:       true,
		},
		{
			name:       "fedora (platform, id 3)",
			teamID:     id.PlatformTeamID,
			templateID: id.FedoraTemplateID,
			want:       true,
		},
		{
			name:       "platform, max reserved id",
			teamID:     id.PlatformTeamID,
			templateID: pgtype.UUID{Bytes: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x04, 0x00}, Valid: true}, // 1024
			want:       true,
		},
		{
			name:       "platform, above reserved range",
			teamID:     id.PlatformTeamID,
			templateID: pgtype.UUID{Bytes: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x04, 0x01}, Valid: true}, // 1025
			want:       false,
		},
		{
			name:       "non-platform team, reserved id",
			teamID:     pgtype.UUID{Bytes: [16]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, Valid: true},
			templateID: id.UbuntuTemplateID,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSystemTemplate(tt.teamID, tt.templateID); got != tt.want {
				t.Errorf("IsSystemTemplate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTemplateDir(t *testing.T) {
	wrennDir := "/var/lib/wrenn"

	t.Run("system base template (ubuntu) lives under teams", func(t *testing.T) {
		got := TemplateDir(wrennDir, id.PlatformTeamID, id.UbuntuTemplateID)
		want := filepath.Join(wrennDir, "images", "teams",
			id.UUIDToBase36(id.PlatformTeamID.Bytes),
			id.UUIDToBase36(id.UbuntuTemplateID.Bytes))
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
	got := TemplateRootfs(wrennDir, id.PlatformTeamID, id.UbuntuTemplateID)
	want := filepath.Join(wrennDir, "images", "teams",
		id.UUIDToBase36(id.PlatformTeamID.Bytes),
		id.UUIDToBase36(id.UbuntuTemplateID.Bytes),
		"rootfs.ext4")
	if got != want {
		t.Errorf("TemplateRootfs() = %q, want %q", got, want)
	}
}

func TestPauseSnapshotDir(t *testing.T) {
	got := PauseSnapshotDir("/var/lib/wrenn", "cl-abc123")
	want := "/var/lib/wrenn/sandboxes/cl-abc123"
	if got != want {
		t.Errorf("PauseSnapshotDir() = %q, want %q", got, want)
	}
}

func TestPauseStagingDir(t *testing.T) {
	got := PauseStagingDir("/var/lib/wrenn", "cl-abc123")
	prefix := "/var/lib/wrenn/sandboxes/cl-abc123.staging-"
	if len(got) <= len(prefix) || got[:len(prefix)] != prefix {
		t.Errorf("PauseStagingDir() = %q, want prefix %q", got, prefix)
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
