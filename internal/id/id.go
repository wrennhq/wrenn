package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewSandboxID generates a new sandbox ID in the format "sb-" + 8 hex chars.
func NewSandboxID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return "sb-" + hex.EncodeToString(b)
}
