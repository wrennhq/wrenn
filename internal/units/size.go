// Package units parses the human-readable size strings used across wrenn's
// configuration and API surfaces (e.g. WRENN_DEFAULT_ROOTFS_SIZE=5Gi, a
// volume's "size": "20Gi"). It deliberately holds no dependencies so both the
// control plane and the host runtime can share one definition of what "5G"
// means.
package units

import (
	"fmt"
	"strconv"
	"strings"
)

// maxSizeMB bounds a parsed size at what fits in an int32 megabyte count (~2
// PiB). Sizes are stored and carried as int32 throughout — DB columns, proto
// fields, request bodies — so a larger value could only reach them by silently
// truncating, turning an absurd request into a plausible-looking small volume.
// Reject it at the parse instead.
const maxSizeMB = 1<<31 - 1

// ParseSizeToMB parses a human-readable size string into megabytes.
// Supported suffixes: G, Gi (gibibytes), M, Mi (mebibytes). A bare number is
// read as megabytes.
// Examples: "5G" → 5120, "2Gi" → 2048, "1000M" → 1000, "512Mi" → 512.
func ParseSizeToMB(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	// Find where the numeric part ends.
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid size %q: no numeric value", s)
	}

	numStr := s[:i]
	suffix := strings.TrimSpace(s[i:])

	num, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}

	var mb float64
	switch suffix {
	case "G", "Gi":
		mb = num * 1024
	case "M", "Mi", "":
		mb = num
	default:
		return 0, fmt.Errorf("invalid size %q: unknown suffix %q (use G, Gi, M, or Mi)", s, suffix)
	}

	// Compare as float: converting first would already have truncated.
	if mb > maxSizeMB {
		return 0, fmt.Errorf("invalid size %q: too large (max %d MB)", s, maxSizeMB)
	}
	return int(mb), nil
}

// FormatMB renders a megabyte count back into the compact human form used in
// error messages, preferring whole gibibytes when the value divides evenly.
func FormatMB(mb int) string {
	if mb > 0 && mb%1024 == 0 {
		return strconv.Itoa(mb/1024) + "Gi"
	}
	return strconv.Itoa(mb) + "Mi"
}
