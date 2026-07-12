package validate

import (
	"fmt"
	"regexp"
)

// emailRe is a pragmatic email check: a local part of common address
// characters, an @, a dotted domain, and a 2+ character TLD. It excludes
// spaces and the HTML-significant characters < > & " ' so a stored email is
// safe to render without escaping.
var emailRe = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

// Email validates that email is a well-formed address within length limits.
// Callers should TrimSpace (and typically ToLower) first.
func Email(email string) error {
	if email == "" {
		return fmt.Errorf("email must not be empty")
	}
	if len(email) > 254 {
		return fmt.Errorf("email is too long (max 254 characters)")
	}
	if !emailRe.MatchString(email) {
		return fmt.Errorf("email is not a valid address")
	}
	return nil
}
