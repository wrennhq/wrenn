package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"git.omukk.dev/wrenn/wrenn/pkg/id"
)

const hostJWTExpiry = 7 * 24 * time.Hour           // 7 days; host refreshes via refresh token
const HostRefreshTokenExpiry = 60 * 24 * time.Hour // 60 days; exported for service layer

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
