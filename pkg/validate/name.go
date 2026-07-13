package validate

import (
	"fmt"
	"regexp"
	"strings"
)

// nameRe matches safe path component names: alphanumeric start, then
// alphanumeric, dash, underscore, or dot. Max 64 characters.
var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// displayNameRe matches user-facing display names (user names, API key names):
// letters, digits, spaces, dots, underscores, hyphens. It deliberately
// excludes the HTML-significant characters < > & " ' so these values are safe
// to render without escaping. Max 100 characters.
var displayNameRe = regexp.MustCompile(`^[A-Za-z0-9 ._-]{1,100}$`)

// disallowedNameChars matches any character not permitted in a display name.
var disallowedNameChars = regexp.MustCompile(`[^A-Za-z0-9 ._-]`)

// DisplayName validates a user-supplied display name. Callers should TrimSpace
// first. It rejects empty names, names over 100 characters, and any character
// outside the alphanumeric + space + . _ - allowlist.
func DisplayName(name string) error {
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if !displayNameRe.MatchString(name) {
		return fmt.Errorf("name may only contain letters, numbers, spaces, and . _ - (max 100 characters)")
	}
	return nil
}

// SanitizeDisplayName coerces an externally-sourced name (e.g. from an OAuth
// provider) into the DisplayName allowlist by dropping disallowed characters.
// Such names cannot be rejected interactively, so they are cleaned instead.
// Returns fallback if nothing usable remains.
func SanitizeDisplayName(name, fallback string) string {
	cleaned := strings.TrimSpace(disallowedNameChars.ReplaceAllString(name, ""))
	if len(cleaned) > 100 {
		cleaned = strings.TrimSpace(cleaned[:100])
	}
	if cleaned == "" {
		return fallback
	}
	return cleaned
}

// SafeName checks that name is safe for use as a single filesystem path
// component. It rejects empty strings, path separators, ".." sequences,
// leading dots, and anything outside the alphanumeric+dash+underscore+dot
// allowlist.
func SafeName(name string) error {
	if name == "" {
		return fmt.Errorf("name must not be empty")
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("name %q contains invalid characters or is too long (max 64, must match %s)", name, nameRe.String())
	}
	return nil
}
