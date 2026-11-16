package fakes

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// Clock is a settable clock.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock reading t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now returns the current fake time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// OAuthGrant is what the fake provider knows about one authorization code.
type OAuthGrant struct {
	// Challenge is the S256 PKCE challenge the flow started with.
	Challenge   string
	RedirectURI string
	Identity    app.ExternalIdentity
}

// OAuthProvider is a fake provider that behaves like a real token endpoint:
// each code works once, only with the verifier whose S256 hash is the
// challenge and the redirect URI the flow started with.
type OAuthProvider struct {
	mu     sync.Mutex
	grants map[string]OAuthGrant
}

// NewOAuthProvider returns a provider with no codes.
func NewOAuthProvider() *OAuthProvider { return &OAuthProvider{grants: map[string]OAuthGrant{}} }

// Grant registers a code, as if the user had just approved the flow.
func (p *OAuthProvider) Grant(code string, g OAuthGrant) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.grants[code] = g
}

// S256 returns the PKCE S256 challenge of a verifier (RFC 7636 4.2).
func S256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Exchange implements app.OAuthProvider.
func (p *OAuthProvider) Exchange(_ context.Context, code, verifier, redirectURI string) (app.ExternalIdentity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	g, ok := p.grants[code]
	if !ok {
		return app.ExternalIdentity{}, fmt.Errorf("fake oauth: unknown code: %w", app.ErrOAuthRejected)
	}
	delete(p.grants, code)
	if S256(verifier) != g.Challenge || redirectURI != g.RedirectURI {
		return app.ExternalIdentity{}, fmt.Errorf("fake oauth: invalid_grant: %w", app.ErrOAuthRejected)
	}
	return g.Identity, nil
}

// Issued is one token the fake issuer signed.
type Issued struct {
	Subject   domain.Subject
	ExpiresAt time.Time
}

// Issuer is a TokenIssuer that records what it signs and returns opaque
// strings; use the real keyring signer when tokens must verify.
type Issuer struct {
	mu     sync.Mutex
	now    func() time.Time
	issued map[string]Issued
	n      int
}

// NewIssuer returns an issuer on clock now.
func NewIssuer(now func() time.Time) *Issuer { return &Issuer{now: now, issued: map[string]Issued{}} }

// Issue implements app.TokenIssuer.
func (i *Issuer) Issue(subject domain.Subject, ttl time.Duration) (string, time.Time, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	tok := fmt.Sprintf("fake-access-%d", i.n)
	exp := i.now().Add(ttl)
	i.issued[tok] = Issued{Subject: subject, ExpiresAt: exp}
	return tok, exp, nil
}

// Lookup returns what a token was issued for.
func (i *Issuer) Lookup(token string) (Issued, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	v, ok := i.issued[token]
	return v, ok
}
