package layout

import (
	"path/filepath"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/internal/id"
)

// IsMinimal reports whether the given team and template IDs represent the
// built-in "minimal" template (both all-zeros).
func IsMinimal(teamID, templateID pgtype.UUID) bool {
	return teamID.Bytes == id.PlatformTeamID.Bytes && templateID.Bytes == id.MinimalTemplateID.Bytes
}

// TemplateDir returns the on-disk directory for a template.
//
//	minimal (zeros, zeros): {wrennDir}/images/minimal
//	all others:             {wrennDir}/images/teams/{base36(teamID)}/{base36(templateID)}
func TemplateDir(wrennDir string, teamID, templateID pgtype.UUID) string {
	if IsMinimal(teamID, templateID) {
		return filepath.Join(wrennDir, "images", "minimal")
	}
	return filepath.Join(wrennDir, "images", "teams",
		id.UUIDToBase36(teamID.Bytes),
		id.UUIDToBase36(templateID.Bytes))
}

// TemplateRootfs returns the path to a template's rootfs.ext4.
func TemplateRootfs(wrennDir string, teamID, templateID pgtype.UUID) string {
	return filepath.Join(TemplateDir(wrennDir, teamID, templateID), "rootfs.ext4")
}

// PauseSnapshotDir returns the directory for a paused sandbox's snapshot files.
func PauseSnapshotDir(wrennDir, sandboxID string) string {
	return filepath.Join(wrennDir, "snapshots", sandboxID)
}

// SandboxesDir returns the directory for running sandbox CoW files.
func SandboxesDir(wrennDir string) string {
	return filepath.Join(wrennDir, "sandboxes")
}

// KernelPath returns the path to the Firecracker kernel.
func KernelPath(wrennDir string) string {
	return filepath.Join(wrennDir, "kernels", "vmlinux")
}

// ImagesRoot returns the root images directory.
func ImagesRoot(wrennDir string) string {
	return filepath.Join(wrennDir, "images")
}

// TeamsDir returns the directory containing all team template subdirectories.
func TeamsDir(wrennDir string) string {
	return filepath.Join(wrennDir, "images", "teams")
}
