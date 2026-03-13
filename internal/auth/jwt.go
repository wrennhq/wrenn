package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const jwtExpiry = 6 * time.Hour

// Claims are the JWT payload.
type Claims struct {
	TeamID string `json:"team_id"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

// SignJWT signs a new 6-hour JWT for the given user.
func SignJWT(secret []byte, userID, teamID, email string) (string, error) {
	now := time.Now()
	claims := Claims{
		TeamID: teamID,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(jwtExpiry)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// VerifyJWT parses and validates a JWT, returning the claims on success.
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
	return *c, nil
}
