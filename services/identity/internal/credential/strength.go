package credential

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Password policy, after NIST SP 800-63B section 5.1.1.2: a minimum length,
// a check against common passwords and the account's own identifiers, and
// no composition rules (they push people towards predictable patterns).
const (
	MinPasswordRunes = 10
	// minDistinctRunes rejects "aaaaaaaaaa" and "abababababab".
	minDistinctRunes = 4
)

// commonPasswords is a small blocklist of the most common passwords that
// meet the length rule. A deployment wanting more can check a breach corpus
// (for example k-anonymity range queries) in front of this.
var commonPasswords = map[string]bool{
	"1234567890": true, "0123456789": true, "qwertyuiop": true, "1q2w3e4r5t": true,
	"password12": true, "password123": true, "password1234": true, "iloveyou12": true,
	"qwerty1234": true, "1qaz2wsx3edc": true, "abcdefghij": true, "letmein123": true,
	"welcome123": true, "administrator": true, "passw0rd123": true, "changeme123": true,
	"qwertyuiop123": true, "123456789a": true, "a123456789": true, "football123": true,
}

// CheckStrength returns ErrWeakPassword (wrapped with the reason) when
// password breaks the policy. email is the account's address; a password
// containing its local part is refused.
func CheckStrength(password, email string) error {
	if !utf8.ValidString(password) {
		return fmt.Errorf("%w: not valid UTF-8", ErrWeakPassword)
	}
	n := utf8.RuneCountInString(password)
	if n < MinPasswordRunes {
		return fmt.Errorf("%w: at least %d characters", ErrWeakPassword, MinPasswordRunes)
	}
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("%w: at most %d bytes", ErrWeakPassword, MaxPasswordBytes)
	}
	distinct := map[rune]bool{}
	for _, r := range password {
		distinct[r] = true
	}
	if len(distinct) < minDistinctRunes {
		return fmt.Errorf("%w: too repetitive", ErrWeakPassword)
	}
	lower := strings.ToLower(password)
	if commonPasswords[lower] {
		return fmt.Errorf("%w: too common", ErrWeakPassword)
	}
	if local, _, ok := strings.Cut(strings.ToLower(email), "@"); ok && len(local) >= 4 && strings.Contains(lower, local) {
		return fmt.Errorf("%w: must not contain your email address", ErrWeakPassword)
	}
	return nil
}
