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

// NewAPIKeyID generates a new API key ID in the format "key-" + 8 hex chars.
func NewAPIKeyID() string {
	return "key-" + hex8()
}
