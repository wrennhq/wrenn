package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/id"
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

// KernelPathVersioned returns the path to a specific kernel version.
func KernelPathVersioned(wrennDir, version string) string {
	return filepath.Join(wrennDir, "kernels", "vmlinux-"+version)
}

// LatestKernel scans the kernels directory for files matching vmlinux-{semver}
// and returns the path and version of the latest one (by semver sort).
func LatestKernel(wrennDir string) (path, version string, err error) {
	dir := filepath.Join(wrennDir, "kernels")
	return latestVersionedFile(dir, "vmlinux-")
}

// latestVersionedFile scans dir for files with the given prefix, extracts the
// version suffix, sorts by semver, and returns the path and version of the latest.
func latestVersionedFile(dir, prefix string) (path, version string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", fmt.Errorf("read directory %s: %w", dir, err)
	}

	var versions []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if v, ok := strings.CutPrefix(name, prefix); ok && v != "" {
			versions = append(versions, v)
		}
	}

	if len(versions) == 0 {
		return "", "", fmt.Errorf("no %s* files found in %s", prefix, dir)
	}

	sort.Slice(versions, func(i, j int) bool {
		return compareSemver(versions[i], versions[j]) < 0
	})

	latest := versions[len(versions)-1]
	return filepath.Join(dir, prefix+latest), latest, nil
}

// compareSemver compares two dotted-numeric version strings.
// Returns -1 if a < b, 0 if equal, 1 if a > b.
func compareSemver(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")

	maxLen := max(len(aParts), len(bParts))

	for i := 0; i < maxLen; i++ {
		var av, bv int
		if i < len(aParts) {
			_, _ = fmt.Sscanf(aParts[i], "%d", &av)
		}
		if i < len(bParts) {
			_, _ = fmt.Sscanf(bParts[i], "%d", &bv)
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

// ImagesRoot returns the root images directory.
func ImagesRoot(wrennDir string) string {
	return filepath.Join(wrennDir, "images")
}

// TeamsDir returns the directory containing all team template subdirectories.
func TeamsDir(wrennDir string) string {
	return filepath.Join(wrennDir, "images", "teams")
}
