package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

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

// NewSnapshotName generates a snapshot name: "template-" + 8 hex chars.
// Templates use TEXT primary keys (not UUID), so this stays as a string.
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
	PrefixSandbox         = "sb-"
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

func formatUUID(prefix string, id pgtype.UUID) string {
	return prefix + uuid.UUID(id.Bytes).String()
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
	u, err := uuid.Parse(strings.TrimPrefix(s, prefix))
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("invalid ID %q: %w", s, err)
	}
	return pgtype.UUID{Bytes: u, Valid: true}, nil
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
