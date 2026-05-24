package layout

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

func timeNowNano() int64 { return time.Now().UnixNano() }

// IsSystemTemplate reports whether the given team and template IDs represent a
// built-in system base template (minimal-ubuntu / -alpine / -arch / -fedora):
// platform-owned with a template ID in the reserved range. System templates are
// protected from deletion.
func IsSystemTemplate(teamID, templateID pgtype.UUID) bool {
	return teamID.Bytes == id.PlatformTeamID.Bytes && id.IsReservedTemplateID(templateID)
}

// TemplateDir returns the on-disk directory for a template. Every template —
// including the built-in system base templates — lives under the teams tree:
//
//	{wrennDir}/images/teams/{base36(teamID)}/{base36(templateID)}
func TemplateDir(wrennDir string, teamID, templateID pgtype.UUID) string {
	return filepath.Join(wrennDir, "images", "teams",
		id.UUIDToBase36(teamID.Bytes),
		id.UUIDToBase36(templateID.Bytes))
}

// TemplateRootfs returns the path to a template's rootfs.ext4.
func TemplateRootfs(wrennDir string, teamID, templateID pgtype.UUID) string {
	return filepath.Join(TemplateDir(wrennDir, teamID, templateID), "rootfs.ext4")
}

// IsSnapshotTemplate reports whether dir contains a Cloud Hypervisor memory
// snapshot (state.json + config.json + memory-ranges) alongside the flattened
// rootfs.ext4. Used to distinguish snapshot templates (launch via CH restore)
// from base/disk-only templates (launch via fresh boot).
//
// state.json is CH-authoritative — its presence indicates a complete snapshot.
func IsSnapshotTemplate(dir string) bool {
	for _, name := range []string{"state.json", "config.json", "rootfs.ext4"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return false
		}
	}
	return true
}

// SandboxCowName is the filename for a sandbox's CoW rootfs diff, kept inside
// the per-sandbox directory alongside any pause snapshot files.
const SandboxCowName = "rootfs.cow"

// SandboxDir returns the per-sandbox directory under sandboxes/. It holds
// the CoW file and, if the sandbox is paused, the snapshot files.
//
// Layout:
//
//	{wrennDir}/sandboxes/{id}/rootfs.cow   CoW file (persistent across pause/resume)
//	{wrennDir}/sandboxes/{id}/             paused snapshot (config.json, state.json, memory-ranges, wrenn-snapshot.json)
//	{wrennDir}/sandboxes/{id}.staging-*/   in-flight Pause writes (cleaned up by swapDir or startup GC)
//	{wrennDir}/sandboxes/{id}.trash-*/     mid-swap previous generation (cleaned up by swapDir or startup GC)
func SandboxDir(wrennDir, sandboxID string) string {
	return filepath.Join(wrennDir, "sandboxes", sandboxID)
}

// SandboxCowPath returns the path to a sandbox's CoW rootfs diff file.
func SandboxCowPath(wrennDir, sandboxID string) string {
	return filepath.Join(SandboxDir(wrennDir, sandboxID), SandboxCowName)
}

// PauseSnapshotDir returns the directory for a paused sandbox's snapshot files.
// Same path as SandboxDir — pause snapshot files live alongside the CoW.
func PauseSnapshotDir(wrennDir, sandboxID string) string {
	return SandboxDir(wrennDir, sandboxID)
}

// PauseStagingDir returns a fresh staging directory for an in-flight Pause.
// Each call returns a unique path (timestamped) so concurrent retries do not
// collide.
func PauseStagingDir(wrennDir, sandboxID string) string {
	return filepath.Join(wrennDir, "sandboxes",
		fmt.Sprintf("%s.staging-%d", sandboxID, timeNowNano()))
}

// SandboxesDir returns the directory for running sandbox CoW files and paused
// snapshot directories.
func SandboxesDir(wrennDir string) string {
	return filepath.Join(wrennDir, "sandboxes")
}

// KernelPath returns the path to the VM kernel.
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
