package auth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Errors returned by this package. Test with errors.Is.
var (
	// ErrInvalidToken means the token is malformed, has a bad signature, uses
	// a forbidden algorithm or type, or fails a claim check other than
	// expiry.
	ErrInvalidToken = errors.New("auth: invalid token")
	// ErrExpired means the token was valid but its exp has passed (or nbf has
	// not arrived). Clients should refresh and retry.
	ErrExpired = errors.New("auth: token expired")
	// ErrUnknownKey means no known public key has the token's key ID.
	ErrUnknownKey = errors.New("auth: unknown signing key")
	// ErrInvalidKeySet means a JWKS document could not be used.
	ErrInvalidKeySet = errors.New("auth: invalid key set")
	// ErrInvalidClaims means Issue was asked for a token it must not make.
	ErrInvalidClaims = errors.New("auth: invalid claims")
)

const (
	algorithm = "EdDSA"
	tokenType = "at+jwt"
	// Issuer is the iss of every Skein access token.
	Issuer = "skein-identity"
	// Audience is accepted by every Skein service.
	Audience = "skein"
	// DefaultLeeway absorbs clock skew between identity and verifiers.
	DefaultLeeway = 30 * time.Second
)

// Subject is who is acting: a user, or an agent run acting for that user.
// It mirrors skein.v1.Subject.
type Subject struct {
	UserID string
	// AgentRunID is set only for delegated tokens.
	AgentRunID string
}

// IsAgent reports whether an agent run is acting for the user.
func (s Subject) IsAgent() bool { return s.AgentRunID != "" }

// Claims are the verified contents of an access token.
type Claims struct {
	Subject   Subject
	Issuer    string
	Audience  []string
	IssuedAt  time.Time
	NotBefore time.Time
	ExpiresAt time.Time
	TokenID   string
	KeyID     string
}

// TokenVerifier checks a bearer token and returns its claims. grpcx and
// httpx authenticate requests through it; Verifier implements it, and tests
// substitute a fake.
type TokenVerifier interface {
	Verify(ctx context.Context, token string) (Claims, error)
}

// wireClaims is the JSON form of Claims.
type wireClaims struct {
	jwt.RegisteredClaims
	AgentRunID string `json:"agent_run_id,omitempty"`
}

// Signer issues tokens with one private key.
type Signer struct {
	key    ed25519.PrivateKey
	keyID  string
	issuer string
	now    func() time.Time
}

// NewSigner returns a signer for key. now is the clock; time.Now when nil.
func NewSigner(key ed25519.PrivateKey, now func() time.Time) *Signer {
	if now == nil {
		now = time.Now
	}
	pub, _ := key.Public().(ed25519.PublicKey)
	return &Signer{key: key, keyID: KeyID(pub), issuer: Issuer, now: now}
}

// KeyID returns the ID of the signing key, as it appears in token headers.
func (s *Signer) KeyID() string { return s.keyID }

// PublicKey returns the verification half of the signing key.
func (s *Signer) PublicKey() ed25519.PublicKey {
	pub, _ := s.key.Public().(ed25519.PublicKey)
	return pub
}

// JWK returns the signing key's public half in its published shape.
func (s *Signer) JWK() JWK { return NewKeySet(s.PublicKey()).JWKS().Keys[0] }

// Issue signs a token for subject, valid for ttl, for the given audiences
// (Audience when none). It returns the token and the claims it carries.
func (s *Signer) Issue(subject Subject, ttl time.Duration, audience ...string) (string, Claims, error) {
	if subject.UserID == "" {
		return "", Claims{}, fmt.Errorf("%w: empty user ID", ErrInvalidClaims)
	}
	if ttl <= 0 {
		return "", Claims{}, fmt.Errorf("%w: non-positive lifetime %v", ErrInvalidClaims, ttl)
	}
	if len(audience) == 0 {
		audience = []string{Audience}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", Claims{}, fmt.Errorf("auth: token id: %w", err)
	}
	// JWT times have one-second resolution; truncate so the returned claims
	// equal what a verifier will read back.
	now := s.now().Truncate(time.Second)
	wire := wireClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.issuer,
			Subject:   subject.UserID,
			Audience:  audience,
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        id.String(),
		},
		AgentRunID: subject.AgentRunID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, wire)
	token.Header["kid"] = s.keyID
	token.Header["typ"] = tokenType
	signed, err := token.SignedString(s.key)
	if err != nil {
		return "", Claims{}, fmt.Errorf("auth: sign: %w", err)
	}
	return signed, fromWire(wire, s.keyID), nil
}

func fromWire(w wireClaims, keyID string) Claims {
	c := Claims{
		Subject:  Subject{UserID: w.Subject, AgentRunID: w.AgentRunID},
		Issuer:   w.Issuer,
		Audience: slices.Clone([]string(w.Audience)),
		TokenID:  w.ID,
		KeyID:    keyID,
	}
	if w.IssuedAt != nil {
		c.IssuedAt = w.IssuedAt.Time
	}
	if w.NotBefore != nil {
		c.NotBefore = w.NotBefore.Time
	}
	if w.ExpiresAt != nil {
		c.ExpiresAt = w.ExpiresAt.Time
	}
	return c
}

// Verifier checks tokens against a KeySource.
type Verifier struct {
	keys     KeySource
	issuer   string
	audience string
	leeway   time.Duration
	now      func() time.Time
}

// VerifierOptions configure NewVerifier. Zero values take the defaults.
type VerifierOptions struct {
	Issuer   string        // Issuer when empty
	Audience string        // Audience when empty
	Leeway   time.Duration // DefaultLeeway when zero
	Now      func() time.Time
}

// NewVerifier returns a verifier that trusts keys from source.
func NewVerifier(source KeySource, opts VerifierOptions) *Verifier {
	v := &Verifier{keys: source, issuer: opts.Issuer, audience: opts.Audience, leeway: opts.Leeway, now: opts.Now}
	if v.issuer == "" {
		v.issuer = Issuer
	}
	if v.audience == "" {
		v.audience = Audience
	}
	if v.leeway == 0 {
		v.leeway = DefaultLeeway
	}
	if v.now == nil {
		v.now = time.Now
	}
	return v
}

// Verify implements TokenVerifier. Expiry and not-before failures return
// ErrExpired; every other failure returns ErrInvalidToken or ErrUnknownKey.
func (v *Verifier) Verify(ctx context.Context, raw string) (Claims, error) {
	var wire wireClaims
	var keyID string
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{algorithm}),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(v.leeway),
		jwt.WithTimeFunc(v.now),
		// Reject base64 with non-zero trailing bits, so a token has exactly
		// one accepted spelling and cannot be altered without detection.
		jwt.WithStrictDecoding(),
	)
	_, err := parser.ParseWithClaims(raw, &wire, func(t *jwt.Token) (any, error) {
		// WithValidMethods has already rejected other algorithms; checking
		// the concrete method again keeps this safe if that option is ever
		// dropped.
		if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		if typ, _ := t.Header["typ"].(string); typ != tokenType {
			return nil, fmt.Errorf("typ %q, want %q", typ, tokenType)
		}
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("missing kid")
		}
		keyID = kid
		return v.keys.PublicKey(ctx, kid)
	})
	switch {
	case err == nil:
	case errors.Is(err, ErrUnknownKey):
		return Claims{}, fmt.Errorf("%w: %q", ErrUnknownKey, keyID)
	case errors.Is(err, jwt.ErrTokenExpired), errors.Is(err, jwt.ErrTokenNotValidYet):
		return Claims{}, fmt.Errorf("%w: %v", ErrExpired, err)
	default:
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if wire.Subject == "" {
		return Claims{}, fmt.Errorf("%w: missing sub", ErrInvalidToken)
	}
	return fromWire(wire, keyID), nil
}
