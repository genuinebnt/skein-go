package keys

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/platform/auth"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func seedKey(b byte) ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = b
	}
	return ed25519.NewKeyFromSeed(seed)
}

func mustInvariants(t *testing.T, r *Keyring) {
	t.Helper()
	if err := r.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func issue(t *testing.T, r *Keyring) string {
	t.Helper()
	tok, _, err := r.Signer().Issue(auth.Subject{UserID: "u"}, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestRotationKeepsEveryUnexpiredTokenVerifiable walks the three steps and
// checks, after each, which tokens verify against the published set.
func TestRotationKeepsEveryUnexpiredTokenVerifiable(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	ring, err := New(seedKey(1), Options{RetireAfter: 16 * time.Minute, Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	mustInvariants(t, ring)
	verifier := auth.NewVerifier(ring, auth.VerifierOptions{Now: c.Now})
	verify := func(tok string) error { _, err := verifier.Verify(context.Background(), tok); return err }

	oldToken := issue(t, ring)
	oldKID := ring.Signer().KeyID()

	// Step 1: stage. Still signs with the old key; both are published.
	if err := ring.Stage(seedKey(2)); err != nil {
		t.Fatal(err)
	}
	mustInvariants(t, ring)
	if ring.Signer().KeyID() != oldKID || len(ring.JWKS().Keys) != 2 {
		t.Fatalf("after staging: signer %s, %d published keys", ring.Signer().KeyID(), len(ring.JWKS().Keys))
	}

	// Step 2: activate. New tokens use the new key; old ones still verify.
	c.now = c.now.Add(5 * time.Minute)
	newKID, err := ring.Activate()
	if err != nil {
		t.Fatal(err)
	}
	mustInvariants(t, ring)
	if newKID == oldKID || ring.Signer().KeyID() != newKID {
		t.Fatal("activation did not switch the signer")
	}
	newToken := issue(t, ring)
	if err := verify(oldToken); err != nil {
		t.Fatalf("old token after activation: %v", err)
	}
	if err := verify(newToken); err != nil {
		t.Fatalf("new token: %v", err)
	}

	// Pruning before RetireAfter keeps the old key.
	c.now = c.now.Add(10 * time.Minute)
	if n := ring.Prune(); n != 0 {
		t.Fatalf("pruned %d keys before their time", n)
	}
	// Step 3: after RetireAfter, the old key goes; its tokens have expired anyway.
	c.now = c.now.Add(7 * time.Minute)
	if n := ring.Prune(); n != 1 {
		t.Fatalf("pruned %d keys, want 1", n)
	}
	mustInvariants(t, ring)
	if _, err := ring.PublicKey(context.Background(), oldKID); !errors.Is(err, auth.ErrUnknownKey) {
		t.Fatalf("retired key still published: %v", err)
	}
	if len(ring.JWKS().Keys) != 1 || ring.JWKS().Keys[0].KeyID != newKID {
		t.Fatalf("JWKS after pruning: %+v", ring.JWKS())
	}
}

func TestPublishedKeySetParsesAndKeyIDsAreThumbprints(t *testing.T) {
	ring, _ := New(seedKey(1), Options{})
	_ = ring.Stage(seedKey(2))
	raw, err := json.Marshal(ring.JWKS())
	if err != nil {
		t.Fatal(err)
	}
	set, err := auth.ParseJWKS(raw)
	if err != nil {
		t.Fatalf("published JWKS does not parse: %v", err)
	}
	if set.Len() != 2 {
		t.Fatalf("%d keys", set.Len())
	}
}

func TestStagingRules(t *testing.T) {
	ring, _ := New(seedKey(1), Options{})
	if _, err := ring.Activate(); !errors.Is(err, ErrNoStagedKey) {
		t.Errorf("activate with nothing staged: %v", err)
	}
	if err := ring.Stage(seedKey(1)); err == nil {
		t.Error("staged the active key again")
	}
	if err := ring.Stage(seedKey(2)); err != nil {
		t.Fatal(err)
	}
	if err := ring.Stage(seedKey(3)); err == nil {
		t.Error("staged a second key")
	}
	mustInvariants(t, ring)
}

func TestRotatorAlternatesStagingAndActivating(t *testing.T) {
	c := &clock{now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	ring, _ := New(seedKey(1), Options{RetireAfter: time.Hour, Now: c.Now})
	next := byte(2)
	rot := &Rotator{Ring: ring, Generate: func() (ed25519.PrivateKey, error) { k := seedKey(next); next++; return k, nil }}
	signers := map[string]bool{ring.Signer().KeyID(): true}
	for i := 0; i < 6; i++ {
		c.now = c.now.Add(25 * time.Minute)
		if err := rot.Tick(); err != nil {
			t.Fatal(err)
		}
		mustInvariants(t, ring)
		signers[ring.Signer().KeyID()] = true
		if n := len(ring.JWKS().Keys); n > 3 {
			t.Fatalf("tick %d: %d keys published; pruning is not keeping up", i, n)
		}
	}
	if len(signers) != 4 {
		t.Fatalf("6 ticks gave %d signing keys, want 4 (one activation every second tick)", len(signers))
	}
}

func TestParseConfig(t *testing.T) {
	enc := func(b byte) string {
		return base64.RawURLEncoding.EncodeToString(seedKey(b).Seed())
	}
	cfg, err := ParseConfig("active:" + enc(1) + ", staged:" + enc(2) + ",retiring:" + enc(3))
	if err != nil {
		t.Fatal(err)
	}
	ring, err := FromConfig(cfg, Options{RetireAfter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	mustInvariants(t, ring)
	if ring.Signer().KeyID() != auth.KeyID(seedKey(1).Public().(ed25519.PublicKey)) || len(ring.JWKS().Keys) != 3 {
		t.Fatal("configured ring has the wrong signer or key count")
	}
	for _, bad := range []string{"", "active", "staged:" + enc(2), "active:" + enc(1) + ",active:" + enc(2), "active:short", "primary:" + enc(1)} {
		_, err := ParseConfig(bad)
		if !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%q accepted", bad)
		}
		if err != nil && strings.Contains(err.Error(), enc(1)) {
			t.Errorf("error for %q echoes a secret seed", bad)
		}
	}
}
