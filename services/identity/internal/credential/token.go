package credential

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// TokenKind is the kind of an opaque token; its prefix is part of the token.
type TokenKind int

// Token kinds.
const (
	KindRefresh TokenKind = iota + 1
	KindShareLink
	KindEmailVerification
	KindPasswordReset
)

// Prefix returns the kind's token prefix.
func (k TokenKind) Prefix() string {
	switch k {
	case KindRefresh:
		return "rt_"
	case KindShareLink:
		return "sl_"
	case KindEmailVerification:
		return "ev_"
	case KindPasswordReset:
		return "pr_"
	default:
		return ""
	}
}

const (
	tokenEntropyBytes = 32
	// tokenBodyLength is the unpadded base64url length of 32 bytes.
	tokenBodyLength = 43
)

// NewToken returns a fresh token of kind k, drawing entropy from random
// (crypto/rand.Reader when nil).
func NewToken(k TokenKind, random io.Reader) (string, error) {
	if k.Prefix() == "" {
		return "", fmt.Errorf("%w: unknown kind %d", ErrMalformedToken, k)
	}
	if random == nil {
		random = rand.Reader
	}
	b := make([]byte, tokenEntropyBytes)
	if _, err := io.ReadFull(random, b); err != nil {
		return "", fmt.Errorf("credential: token entropy: %w", err)
	}
	return k.Prefix() + base64.RawURLEncoding.EncodeToString(b), nil
}

// ParseToken checks that raw is a well-formed token of kind k and returns
// it unchanged. It accepts exactly what NewToken produces: the prefix, then
// 43 characters of canonical unpadded base64url. Checking the shape before
// any lookup keeps garbage out of database queries and logs.
func ParseToken(k TokenKind, raw string) (string, error) {
	prefix := k.Prefix()
	if prefix == "" {
		return "", fmt.Errorf("%w: unknown kind %d", ErrMalformedToken, k)
	}
	body, ok := strings.CutPrefix(raw, prefix)
	if !ok || len(body) != tokenBodyLength || !utf8.ValidString(body) {
		return "", ErrMalformedToken
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(body)
	// The re-encoding check rejects the '\r' and '\n' the decoder skips.
	if err != nil || len(b) != tokenEntropyBytes || base64.RawURLEncoding.EncodeToString(b) != body {
		return "", ErrMalformedToken
	}
	return raw, nil
}

// HashToken returns the lowercase hex SHA-256 of a token, the only form
// identity stores.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
