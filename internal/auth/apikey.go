package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// GenerateAPIKey returns a plaintext key in the form "wrn_" + 32 random hex chars
// and its SHA-256 hash. The caller must show the plaintext to the user exactly once;
// only the hash is stored.
func GenerateAPIKey() (plaintext, hash string, err error) {
	b := make([]byte, 16) // 16 bytes → 32 hex chars
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate api key: %w", err)
	}
	plaintext = "wrn_" + hex.EncodeToString(b)
	hash = HashAPIKey(plaintext)
	return plaintext, hash, nil
}

// HashAPIKey returns the hex-encoded SHA-256 hash of a plaintext API key.
func HashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// APIKeyPrefix returns the displayable prefix of an API key (e.g. "wrn_ab12...").
func APIKeyPrefix(plaintext string) string {
	if len(plaintext) > 12 {
		return plaintext[:12] + "..."
	}
	return plaintext
}
