package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	base36Alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	base36IDLen    = 25 // ceil(128 * log2 / log36) = 25 chars for a full UUID
)

var base36Base = big.NewInt(36)

// --- Generation ---

// newUUID returns a new random (v4) UUID wrapped in pgtype.UUID for direct DB use.
func newUUID() pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.New(), Valid: true}
}

func NewSandboxID() pgtype.UUID         { return newUUID() }
func NewUserID() pgtype.UUID            { return newUUID() }
func NewTeamID() pgtype.UUID            { return newUUID() }
func NewAPIKeyID() pgtype.UUID          { return newUUID() }
func NewHostID() pgtype.UUID            { return newUUID() }
func NewHostTokenID() pgtype.UUID       { return newUUID() }
func NewRefreshTokenID() pgtype.UUID    { return newUUID() }
func NewAuditLogID() pgtype.UUID        { return newUUID() }
func NewBuildID() pgtype.UUID           { return newUUID() }
func NewAdminPermissionID() pgtype.UUID { return newUUID() }

func NewTemplateID() pgtype.UUID { return newUUID() }

// NewSnapshotName generates a snapshot name: "template-" + 8 hex chars.
func NewSnapshotName() string {
	return "template-" + hex8()
}

// NewTeamSlug generates a unique team slug in the format "xxxxxx-yyyyyy".
func NewTeamSlug() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b[:3]) + "-" + hex.EncodeToString(b[3:])
}

// NewRegistrationToken generates a 64-char hex token (32 bytes of entropy).
func NewRegistrationToken() string {
	return hexToken(32)
}

// NewRefreshToken generates a 64-char hex token (32 bytes of entropy).
func NewRefreshToken() string {
	return hexToken(32)
}

// --- Formatting (pgtype.UUID → prefixed string for API/RPC output) ---

const (
	PrefixSandbox         = "cl-"
	PrefixUser            = "usr-"
	PrefixTeam            = "team-"
	PrefixAPIKey          = "key-"
	PrefixHost            = "host-"
	PrefixHostToken       = "htok-"
	PrefixRefreshToken    = "hrt-"
	PrefixAuditLog        = "log-"
	PrefixBuild           = "bld-"
	PrefixAdminPermission = "perm-"
)

// UUIDToBase36 encodes 16 UUID bytes as a 25-char base36 string (0-9a-z).
func UUIDToBase36(b [16]byte) string {
	n := new(big.Int).SetBytes(b[:])
	buf := make([]byte, base36IDLen)
	mod := new(big.Int)
	for i := base36IDLen - 1; i >= 0; i-- {
		n.DivMod(n, base36Base, mod)
		buf[i] = base36Alphabet[mod.Int64()]
	}
	return string(buf)
}

// base36ToUUID decodes a 25-char base36 string back to 16 UUID bytes.
func base36ToUUID(s string) ([16]byte, error) {
	if len(s) != base36IDLen {
		return [16]byte{}, fmt.Errorf("expected %d-char base36 ID, got %d", base36IDLen, len(s))
	}
	n := new(big.Int)
	for _, c := range s {
		idx := strings.IndexRune(base36Alphabet, c)
		if idx < 0 {
			return [16]byte{}, fmt.Errorf("invalid base36 character: %c", c)
		}
		n.Mul(n, base36Base)
		n.Add(n, big.NewInt(int64(idx)))
	}
	b := n.Bytes()
	var out [16]byte
	// big.Int.Bytes() strips leading zeros; right-align into 16-byte array.
	copy(out[16-len(b):], b)
	return out, nil
}

func formatUUID(prefix string, id pgtype.UUID) string {
	return prefix + UUIDToBase36(id.Bytes)
}

func FormatSandboxID(id pgtype.UUID) string      { return formatUUID(PrefixSandbox, id) }
func FormatUserID(id pgtype.UUID) string         { return formatUUID(PrefixUser, id) }
func FormatTeamID(id pgtype.UUID) string         { return formatUUID(PrefixTeam, id) }
func FormatAPIKeyID(id pgtype.UUID) string       { return formatUUID(PrefixAPIKey, id) }
func FormatHostID(id pgtype.UUID) string         { return formatUUID(PrefixHost, id) }
func FormatHostTokenID(id pgtype.UUID) string    { return formatUUID(PrefixHostToken, id) }
func FormatRefreshTokenID(id pgtype.UUID) string { return formatUUID(PrefixRefreshToken, id) }
func FormatAuditLogID(id pgtype.UUID) string     { return formatUUID(PrefixAuditLog, id) }
func FormatBuildID(id pgtype.UUID) string        { return formatUUID(PrefixBuild, id) }

// --- Parsing (prefixed string from API/RPC input → pgtype.UUID) ---

func parseUUID(prefix, s string) (pgtype.UUID, error) {
	if !strings.HasPrefix(s, prefix) {
		return pgtype.UUID{}, fmt.Errorf("invalid ID: expected %q prefix, got %q", prefix, s)
	}
	b, err := base36ToUUID(strings.TrimPrefix(s, prefix))
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid ID %q: %w", s, err)
	}
	return pgtype.UUID{Bytes: b, Valid: true}, nil
}

func ParseSandboxID(s string) (pgtype.UUID, error)   { return parseUUID(PrefixSandbox, s) }
func ParseUserID(s string) (pgtype.UUID, error)      { return parseUUID(PrefixUser, s) }
func ParseTeamID(s string) (pgtype.UUID, error)      { return parseUUID(PrefixTeam, s) }
func ParseAPIKeyID(s string) (pgtype.UUID, error)    { return parseUUID(PrefixAPIKey, s) }
func ParseHostID(s string) (pgtype.UUID, error)      { return parseUUID(PrefixHost, s) }
func ParseHostTokenID(s string) (pgtype.UUID, error) { return parseUUID(PrefixHostToken, s) }
func ParseAuditLogID(s string) (pgtype.UUID, error)  { return parseUUID(PrefixAuditLog, s) }
func ParseBuildID(s string) (pgtype.UUID, error)     { return parseUUID(PrefixBuild, s) }

// --- Well-known IDs ---

// PlatformTeamID is the all-zeros UUID reserved for platform-owned resources
// (e.g. base templates, shared infrastructure).
var PlatformTeamID = pgtype.UUID{Bytes: [16]byte{}, Valid: true}

// MinimalTemplateID is the all-zeros UUID sentinel for the built-in "minimal"
// template. When both team_id and template_id are zero, the host agent uses
// the minimal rootfs at WRENN_DIR/images/minimal/.
var MinimalTemplateID = pgtype.UUID{Bytes: [16]byte{}, Valid: true}

// UUIDString converts a pgtype.UUID to a standard hyphenated UUID string
// (e.g., "6ba7b810-9dad-11d1-80b4-00c04fd430c8"). Used for RPC wire format.
func UUIDString(id pgtype.UUID) string {
	return uuid.UUID(id.Bytes).String()
}

// --- Helpers ---

func hex8() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

func hexToken(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
