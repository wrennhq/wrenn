package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/sandbox/internal/id"
)

const jwtExpiry = 6 * time.Hour
const hostJWTExpiry = 7 * 24 * time.Hour           // 7 days; host refreshes via refresh token
const HostRefreshTokenExpiry = 60 * 24 * time.Hour // 60 days; exported for service layer

// Claims are the JWT payload for user tokens.
type Claims struct {
	Type    string `json:"typ,omitempty"` // empty for user tokens; used to reject host tokens
	TeamID  string `json:"team_id"`
	Role    string `json:"role"` // owner, admin, or member within TeamID
	Email   string `json:"email"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"is_admin,omitempty"` // platform-level admin flag
	jwt.RegisteredClaims
}

// SignJWT signs a new 6-hour JWT for the given user.
func SignJWT(secret []byte, userID, teamID pgtype.UUID, email, name, role string, isAdmin bool) (string, error) {
	now := time.Now()
	claims := Claims{
		TeamID:  id.FormatTeamID(teamID),
		Role:    role,
		Email:   email,
		Name:    name,
		IsAdmin: isAdmin,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   id.FormatUserID(userID),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(jwtExpiry)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// VerifyJWT parses and validates a user JWT, returning the claims on success.
// Rejects host JWTs (which carry a "typ" claim) to prevent cross-token confusion.
func VerifyJWT(secret []byte, tokenStr string) (Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return Claims{}, fmt.Errorf("invalid token: %w", err)
	}
	c, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return Claims{}, fmt.Errorf("invalid token claims")
	}
	if c.Type == "host" {
		return Claims{}, fmt.Errorf("invalid token: host token cannot be used as user token")
	}
	return *c, nil
}

// HostClaims are the JWT payload for host agent tokens.
type HostClaims struct {
	Type   string `json:"typ"` // always "host"
	HostID string `json:"host_id"`
	jwt.RegisteredClaims
}

// SignHostJWT signs a long-lived (7-day) JWT for a registered host agent.
func SignHostJWT(secret []byte, hostID pgtype.UUID) (string, error) {
	formatted := id.FormatHostID(hostID)
	now := time.Now()
	claims := HostClaims{
		Type:   "host",
		HostID: formatted,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   formatted,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(hostJWTExpiry)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// VerifyHostJWT parses and validates a host JWT, returning the claims on success.
// It rejects user JWTs by checking the "typ" claim.
func VerifyHostJWT(secret []byte, tokenStr string) (HostClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &HostClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return HostClaims{}, fmt.Errorf("invalid token: %w", err)
	}
	c, ok := token.Claims.(*HostClaims)
	if !ok || !token.Valid {
		return HostClaims{}, fmt.Errorf("invalid token claims")
	}
	if c.Type != "host" {
		return HostClaims{}, fmt.Errorf("invalid token type: expected host")
	}
	return *c, nil
}
