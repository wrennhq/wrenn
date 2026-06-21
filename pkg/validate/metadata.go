package validate

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

// metadataKeyRe matches user metadata keys: alphanumeric start, then
// alphanumeric, dash, underscore, or dot. Max 64 characters. Mirrors the
// SafeName allowlist so keys stay predictable across the API and UI.
var metadataKeyRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

const (
	// MaxMetadataKeys caps how many user labels a sandbox may carry.
	MaxMetadataKeys = 20
	// MaxMetadataValueLen caps the length (in characters) of a label value.
	MaxMetadataValueLen = 64
)

// reservedMetadataKeys are written by the host agent after a VM boots and must
// not be set by users — they carry the immutable system facts about the VM.
// Keep in sync with (*sandbox.Manager).buildMetadata: if a key is added there
// but not here, a user could set it at create-time and watch it silently get
// overwritten by the agent on boot. (validate cannot import internal/sandbox,
// hence the duplication — same pattern as service.MinTimeoutSec.)
var reservedMetadataKeys = map[string]struct{}{
	"kernel_version": {},
	"vmm_version":    {},
	"agent_version":  {},
	"envd_version":   {},
}

// Metadata validates a user-supplied sandbox metadata map. It rejects reserved
// system keys, enforces a key count limit, and constrains key/value shape so
// the data stays safe to render and store. A nil/empty map is valid.
func Metadata(meta map[string]string) error {
	if len(meta) > MaxMetadataKeys {
		return fmt.Errorf("too many metadata keys: %d (max %d)", len(meta), MaxMetadataKeys)
	}
	for k, v := range meta {
		if _, reserved := reservedMetadataKeys[k]; reserved {
			return fmt.Errorf("metadata key %q is reserved for system use", k)
		}
		if !metadataKeyRe.MatchString(k) {
			return fmt.Errorf("metadata key %q is invalid (max 64 chars, must match %s)", k, metadataKeyRe.String())
		}
		if utf8.RuneCountInString(v) > MaxMetadataValueLen {
			return fmt.Errorf("metadata value for key %q is too long (max %d chars)", k, MaxMetadataValueLen)
		}
	}
	return nil
}
