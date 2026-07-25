package validate

import (
	"fmt"
	"strings"
)

// VolumeNamePrefix is the mandatory leading marker on every volume name. It
// makes a name self-describing in logs, mount paths, and SDK code, and — since
// volume IDs carry the distinct "vol-" prefix — lets a single API path segment
// be resolved as either an ID or a name without ambiguity.
const VolumeNamePrefix = "vl-"

// VolumeName normalizes and validates a user-supplied volume name. The name
// body follows the same rules as a team slug (lowercase alphanumerics in
// dash-separated groups) so names stay safe in paths, URLs, and shell contexts.
//
// The prefix is optional on input: both "cache" and "vl-cache" normalize to
// "vl-cache", so an SDK caller who forgets it still gets the volume they meant.
// The returned name is always the prefixed, lower-cased form and is what
// callers should store and compare against.
func VolumeName(name string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return "", fmt.Errorf("volume name must not be empty")
	}
	normalized = strings.TrimPrefix(normalized, VolumeNamePrefix)
	if normalized == "" {
		return "", fmt.Errorf("volume name must have something after %q", VolumeNamePrefix)
	}

	normalized = VolumeNamePrefix + normalized
	// The whole prefixed name is capped at 40 characters, which comfortably
	// fits the default "vl-" + 25-char base36 ID form (28 characters).
	if len(normalized) > 40 {
		return "", fmt.Errorf("volume name must be at most 40 characters including the %q prefix", VolumeNamePrefix)
	}
	// slugRe is shared with team slugs: lowercase alphanumerics in
	// dash-separated groups, no leading, trailing, or repeated dashes.
	if !slugRe.MatchString(normalized) {
		return "", fmt.Errorf("volume name may only contain lowercase letters, numbers, and dashes (no leading, trailing, or repeated dashes)")
	}
	return normalized, nil
}
