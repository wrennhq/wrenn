package auth

import "golang.org/x/crypto/bcrypt"

const bcryptCost = 12

// HashPassword returns the bcrypt hash of a plaintext password.
func HashPassword(plaintext string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	return string(b), err
}

// CheckPassword returns nil if plaintext matches the stored hash.
func CheckPassword(hash, plaintext string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
}
