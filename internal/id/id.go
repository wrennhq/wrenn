package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func hex8() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// NewSandboxID generates a new sandbox ID in the format "sb-" + 8 hex chars.
func NewSandboxID() string {
	return "sb-" + hex8()
}

// NewSnapshotName generates a snapshot name in the format "template-" + 8 hex chars.
func NewSnapshotName() string {
	return "template-" + hex8()
}

// NewUserID generates a new user ID in the format "usr-" + 8 hex chars.
func NewUserID() string {
	return "usr-" + hex8()
}

// NewTeamID generates a new team ID in the format "team-" + 8 hex chars.
func NewTeamID() string {
	return "team-" + hex8()
}

// NewTeamSlug generates a unique team slug in the format "xxxxxx-yyyyyy"
// where each part is 3 random bytes encoded as hex (6 hex chars each).
func NewTeamSlug() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b[:3]) + "-" + hex.EncodeToString(b[3:])
}

// NewAPIKeyID generates a new API key ID in the format "key-" + 8 hex chars.
func NewAPIKeyID() string {
	return "key-" + hex8()
}

// NewHostID generates a new host ID in the format "host-" + 8 hex chars.
func NewHostID() string {
	return "host-" + hex8()
}

// NewHostTokenID generates a new host token audit ID in the format "htok-" + 8 hex chars.
func NewHostTokenID() string {
	return "htok-" + hex8()
}

// NewRegistrationToken generates a 64-char hex token (32 bytes of entropy).
func NewRegistrationToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// NewRefreshTokenID generates a new refresh token record ID in the format "hrt-" + 8 hex chars.
func NewRefreshTokenID() string {
	return "hrt-" + hex8()
}

// NewAuditLogID generates a new audit log ID in the format "log-" + 8 hex chars.
func NewAuditLogID() string {
	return "log-" + hex8()
}

// NewRefreshToken generates a 64-char hex token (32 bytes of entropy) for use as a host refresh token.
func NewRefreshToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
