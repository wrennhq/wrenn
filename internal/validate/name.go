package validate

import (
	"fmt"
	"regexp"
)

// nameRe matches safe path component names: alphanumeric start, then
// alphanumeric, dash, underscore, or dot. Max 64 characters.
var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

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
