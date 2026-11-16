package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newKey(t testing.TB) ed25519.PrivateKey {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func setup(t testing.TB) (*Signer, *Verifier, *clock) {
	t.Helper()
	c := &clock{now: t0}
	s := NewSigner(newKey(t), c.Now)
	v := NewVerifier(NewKeySet(s.PublicKey()), VerifierOptions{Now: c.Now})
	return s, v, c
}

func issue(t testing.TB, s *Signer, sub Subject, ttl time.Duration) string {
	t.Helper()
	tok, _, err := s.Issue(sub, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func b64(v any) string {
	data, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(data)
}

func TestIssueAndVerifyRoundTripsUserAndAgentClaims(t *testing.T) {
	s, v, _ := setup(t)
	for _, sub := range []Subject{{UserID: "u1"}, {UserID: "u1", AgentRunID: "run-9"}} {
		tok, issued, err := s.Issue(sub, 15*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		got, err := v.Verify(context.Background(), tok)
		if err != nil {
			t.Fatalf("Verify(%+v): %v", sub, err)
		}
		if got.Subject != sub || got.KeyID != s.KeyID() || got.TokenID == "" || got.TokenID != issued.TokenID {
			t.Errorf("claims %+v, want subject %+v from key %s", got, sub, s.KeyID())
		}
		if !got.ExpiresAt.Equal(t0.Add(15*time.Minute)) || got.Subject.IsAgent() != (sub.AgentRunID != "") {
			t.Errorf("claims %+v", got)
		}
	}
}

func TestExpiredTokenIsRejectedAfterLeeway(t *testing.T) {
	s, v, c := setup(t)
	tok := issue(t, s, Subject{UserID: "u1"}, time.Minute)

	c.now = t0.Add(time.Minute + DefaultLeeway - time.Second)
	if _, err := v.Verify(context.Background(), tok); err != nil {
		t.Fatalf("within leeway: %v", err)
	}
	c.now = t0.Add(time.Minute + DefaultLeeway + time.Second)
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrExpired) {
		t.Fatalf("after leeway: want ErrExpired, got %v", err)
	}
}

func TestTokenFromTheFutureIsRejected(t *testing.T) {
	s, v, c := setup(t)
	tok := issue(t, s, Subject{UserID: "u1"}, time.Hour)
	c.now = t0.Add(-DefaultLeeway - time.Minute)
	if _, err := v.Verify(context.Background(), tok); err == nil {
		t.Fatal("token not yet valid was accepted")
	}
}

func TestTokenSignedByUnknownKeyIsRejected(t *testing.T) {
	_, v, c := setup(t)
	stranger := NewSigner(newKey(t), c.Now)
	tok := issue(t, stranger, Subject{UserID: "u1"}, time.Hour)
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("want ErrUnknownKey, got %v", err)
	}
}

func TestTokenSignedByWrongKeyUnderTrustedKidIsRejected(t *testing.T) {
	s, v, c := setup(t)
	stranger := NewSigner(newKey(t), c.Now)
	stranger.keyID = s.KeyID() // claim to be the trusted key
	tok := issue(t, stranger, Subject{UserID: "u1"}, time.Hour)
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("want ErrInvalidToken, got %v", err)
	}
}

func TestTamperedPayloadIsRejected(t *testing.T) {
	s, v, _ := setup(t)
	tok := issue(t, s, Subject{UserID: "victim"}, time.Hour)
	parts := strings.Split(tok, ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	forged := strings.Replace(string(payload), `"sub":"victim"`, `"sub":"attacker"`, 1)
	if forged == string(payload) {
		t.Fatal("test did not change the payload")
	}
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(forged))
	if _, err := v.Verify(context.Background(), strings.Join(parts, ".")); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("want ErrInvalidToken, got %v", err)
	}

	// Flipping one bit of the signature must also fail.
	parts = strings.Split(tok, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[0] ^= 1
	parts[2] = base64.RawURLEncoding.EncodeToString(sig)
	if _, err := v.Verify(context.Background(), strings.Join(parts, ".")); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("bit flip: want ErrInvalidToken, got %v", err)
	}
}

// validPayload returns claims that would pass every check if signed
// properly, so the only thing under test is the header and signature.
func validPayload() map[string]any {
	return map[string]any{
		"iss": Issuer, "sub": "attacker", "aud": []string{Audience},
		"iat": t0.Unix(), "nbf": t0.Unix(), "exp": t0.Add(time.Hour).Unix(),
	}
}

func TestAlgorithmConfusionIsRejected(t *testing.T) {
	s, v, _ := setup(t)
	pub := s.PublicKey()
	hs256 := func(header map[string]any) string {
		signingInput := b64(header) + "." + b64(validPayload())
		mac := hmac.New(sha256.New, pub) // the public key as an HMAC secret
		mac.Write([]byte(signingInput))
		return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	cases := map[string]string{
		"alg none":            b64(map[string]any{"alg": "none", "typ": tokenType, "kid": s.KeyID()}) + "." + b64(validPayload()) + ".",
		"alg none, no sig":    b64(map[string]any{"alg": "none", "typ": tokenType}) + "." + b64(validPayload()),
		"HS256 with pub key":  hs256(map[string]any{"alg": "HS256", "typ": tokenType, "kid": s.KeyID()}),
		"lowercase eddsa":     hs256(map[string]any{"alg": "eddsa", "typ": tokenType, "kid": s.KeyID()}),
		"RS256 header":        hs256(map[string]any{"alg": "RS256", "typ": tokenType, "kid": s.KeyID()}),
		"empty alg":           hs256(map[string]any{"alg": "", "typ": tokenType, "kid": s.KeyID()}),
		"jku header injected": hs256(map[string]any{"alg": "HS256", "jku": "https://evil.example/jwks", "kid": s.KeyID()}),
	}
	for name, tok := range cases {
		if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: want ErrInvalidToken, got %v", name, err)
		}
	}
}

func TestWrongTypeIssuerOrAudienceIsRejected(t *testing.T) {
	c := &clock{now: t0}
	key := newKey(t)
	s := NewSigner(key, c.Now)
	keys := NewKeySet(s.PublicKey())
	v := NewVerifier(keys, VerifierOptions{Now: c.Now})

	// An otherwise valid EdDSA token whose typ is not at+jwt (an ID token).
	header := map[string]any{"alg": algorithm, "typ": "JWT", "kid": s.KeyID()}
	input := b64(header) + "." + b64(validPayload())
	idToken := input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
	if _, err := v.Verify(context.Background(), idToken); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("typ JWT: want ErrInvalidToken, got %v", err)
	}

	tok, _, _ := s.Issue(Subject{UserID: "u1"}, time.Hour, "other-system")
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("wrong audience: want ErrInvalidToken, got %v", err)
	}

	s.issuer = "someone-else"
	tok = issue(t, s, Subject{UserID: "u1"}, time.Hour)
	if _, err := v.Verify(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("wrong issuer: want ErrInvalidToken, got %v", err)
	}
}

func TestIssueRejectsEmptySubjectAndLifetime(t *testing.T) {
	s, _, _ := setup(t)
	if _, _, err := s.Issue(Subject{}, time.Hour); !errors.Is(err, ErrInvalidClaims) {
		t.Errorf("empty subject: %v", err)
	}
	if _, _, err := s.Issue(Subject{UserID: "u"}, 0); !errors.Is(err, ErrInvalidClaims) {
		t.Errorf("zero ttl: %v", err)
	}
}

func TestRotationKeepsOldTokensValidUntilTheOldKeyIsRemoved(t *testing.T) {
	c := &clock{now: t0}
	oldSigner := NewSigner(newKey(t), c.Now)
	oldToken := issue(t, oldSigner, Subject{UserID: "u1"}, time.Hour)

	// Step 1: publish the new key alongside the old.
	newSigner := NewSigner(newKey(t), c.Now)
	keys := NewKeySet(oldSigner.PublicKey()).With(newSigner.PublicKey())
	v := NewVerifier(keys, VerifierOptions{Now: c.Now})
	// Step 2: sign with the new key; both tokens verify.
	newToken := issue(t, newSigner, Subject{UserID: "u1"}, time.Hour)
	for name, tok := range map[string]string{"old": oldToken, "new": newToken} {
		if _, err := v.Verify(context.Background(), tok); err != nil {
			t.Errorf("%s token during rotation: %v", name, err)
		}
	}
	// Step 3: retire the old key; old tokens stop verifying.
	v = NewVerifier(keys.Without(oldSigner.KeyID()), VerifierOptions{Now: c.Now})
	if _, err := v.Verify(context.Background(), oldToken); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("old token after retirement: want ErrUnknownKey, got %v", err)
	}
	if _, err := v.Verify(context.Background(), newToken); err != nil {
		t.Errorf("new token after retirement: %v", err)
	}
}

func TestJWKSRoundTripsAndKeyIDIsTheThumbprint(t *testing.T) {
	s1, s2 := NewSigner(newKey(t), nil), NewSigner(newKey(t), nil)
	set := NewKeySet(s1.PublicKey(), s2.PublicKey())
	data, err := json.Marshal(set.JWKS())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseJWKS(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Signer{s1, s2} {
		k, err := parsed.PublicKey(context.Background(), s.KeyID())
		if err != nil || !k.Equal(s.PublicKey()) {
			t.Errorf("key %s lost in round trip: %v", s.KeyID(), err)
		}
	}
	// RFC 8037 appendix A.3 gives this thumbprint for its example key.
	x, _ := base64.RawURLEncoding.DecodeString("11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo")
	if got := KeyID(x); got != "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k" {
		t.Errorf("KeyID = %s, want the RFC 8037 thumbprint", got)
	}
}

func TestParseJWKSRejectsMalformedKeys(t *testing.T) {
	s := NewSigner(newKey(t), nil)
	good := s.JWK()
	mutate := func(f func(*JWK)) []byte {
		k := good
		f(&k)
		data, _ := json.Marshal(JWKS{Keys: []JWK{k}})
		return data
	}
	cases := map[string][]byte{
		"not json":     []byte("{"),
		"rsa key":      mutate(func(k *JWK) { k.KeyType = "RSA" }),
		"wrong curve":  mutate(func(k *JWK) { k.Curve = "X25519" }),
		"hmac alg":     mutate(func(k *JWK) { k.Algorithm = "HS256" }),
		"enc use":      mutate(func(k *JWK) { k.Use = "enc" }),
		"short x":      mutate(func(k *JWK) { k.X = "AAAA" }),
		"mislabel kid": mutate(func(k *JWK) { k.KeyID = "other" }),
	}
	for name, data := range cases {
		if _, err := ParseJWKS(data); !errors.Is(err, ErrInvalidKeySet) {
			t.Errorf("%s: want ErrInvalidKeySet, got %v", name, err)
		}
	}
}

func TestRemoteKeySetCachesRefetchesOnUnknownKidAndSurvivesOutages(t *testing.T) {
	c := &clock{now: t0}
	first, second := NewSigner(newKey(t), c.Now), NewSigner(newKey(t), c.Now)
	published := NewKeySet(first.PublicKey())
	var fetches atomic.Int32
	var failing atomic.Bool
	fetch := func(context.Context) ([]byte, error) {
		fetches.Add(1)
		if failing.Load() {
			return nil, errors.New("identity is down")
		}
		return json.Marshal(published.JWKS())
	}
	remote := NewRemoteKeySet(fetch, RemoteKeySetOptions{RefreshInterval: time.Minute, MinRefetch: 10 * time.Second, Now: c.Now})
	ctx := context.Background()

	for range 3 {
		if _, err := remote.PublicKey(ctx, first.KeyID()); err != nil {
			t.Fatal(err)
		}
	}
	if fetches.Load() != 1 {
		t.Fatalf("cached lookups fetched %d times, want 1", fetches.Load())
	}

	// Rotation step 1 happened after our fetch: the unknown kid triggers an
	// early refetch, at most once per MinRefetch.
	published = published.With(second.PublicKey())
	c.now = c.now.Add(11 * time.Second)
	if _, err := remote.PublicKey(ctx, second.KeyID()); err != nil {
		t.Fatalf("new kid after refetch: %v", err)
	}
	for range 5 {
		_, _ = remote.PublicKey(ctx, "random-kid")
	}
	if fetches.Load() != 2 {
		t.Fatalf("unknown kids caused %d fetches, want 2 in total", fetches.Load())
	}

	// identity goes down: the last good set keeps working past its
	// refresh interval.
	failing.Store(true)
	c.now = c.now.Add(2 * time.Minute)
	if _, err := remote.PublicKey(ctx, first.KeyID()); err != nil {
		t.Fatalf("outage must not drop known keys: %v", err)
	}
}

func TestRemoteKeySetReportsFetchFailureBeforeFirstLoad(t *testing.T) {
	remote := NewRemoteKeySet(func(context.Context) ([]byte, error) { return nil, errors.New("down") }, RemoteKeySetOptions{})
	if _, err := remote.PublicKey(context.Background(), "kid"); !errors.Is(err, ErrUnknownKey) || !strings.Contains(err.Error(), "down") {
		t.Fatalf("got %v", err)
	}
}

func TestContextCarriesClaimsAndToken(t *testing.T) {
	ctx := context.Background()
	if _, ok := SubjectFromContext(ctx); ok {
		t.Fatal("empty context has a subject")
	}
	ctx = WithClaims(ctx, Claims{Subject: Subject{UserID: "u1"}}, "raw")
	sub, ok := SubjectFromContext(ctx)
	tok, tokOK := TokenFromContext(ctx)
	if !ok || sub.UserID != "u1" || !tokOK || tok != "raw" {
		t.Fatalf("got %+v %v %q %v", sub, ok, tok, tokOK)
	}
}

// FuzzParseJWKS checks that arbitrary key set documents never panic and
// that anything accepted contains only keys whose ID is their thumbprint.
func FuzzParseJWKS(f *testing.F) {
	s := NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), nil)
	good, _ := json.Marshal(NewKeySet(s.PublicKey()).JWKS())
	for _, seed := range [][]byte{good, []byte(`{"keys":[]}`), []byte(`{"keys":[{"kty":"OKP"}]}`), []byte("null")} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		set, err := ParseJWKS(data)
		if err != nil {
			return
		}
		for id, k := range set.keys {
			if len(k) != ed25519.PublicKeySize || KeyID(k) != id {
				t.Fatalf("accepted bad key %q", id)
			}
		}
	})
}

// FuzzVerify checks that arbitrary strings never panic the verifier and
// are never accepted unless they are byte-for-byte a token the signer made.
func FuzzVerify(f *testing.F) {
	c := &clock{now: t0}
	s := NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)), c.Now)
	v := NewVerifier(NewKeySet(s.PublicKey()), VerifierOptions{Now: c.Now})
	// Fuzz workers are separate processes, so the genuine token must be the
	// same in each: fixed key, fixed claims and Ed25519's deterministic
	// signatures make it so (Issue would mint a random jti).
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	payload := validPayload()
	payload["sub"], payload["jti"] = "u1", "fixed"
	input := b64(map[string]any{"alg": algorithm, "typ": tokenType, "kid": s.KeyID()}) + "." + b64(payload)
	genuine := input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))
	if _, err := v.Verify(context.Background(), genuine); err != nil {
		f.Fatalf("hand-built token must verify: %v", err)
	}
	for _, seed := range []string{genuine, "", "a.b.c", "..", genuine + "x", strings.ToUpper(genuine)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		claims, err := v.Verify(context.Background(), raw)
		if err != nil {
			return
		}
		// Ed25519 signatures are not malleable and base64url decoding in
		// golang-jwt is strict, so only the genuine token can pass.
		if raw != genuine {
			t.Fatalf("accepted a token the signer never issued: %q (%+v)", raw, claims)
		}
	})
}

func TestNonCanonicalBase64SpellingIsRejected(t *testing.T) {
	s, v, _ := setup(t)
	tok := issue(t, s, Subject{UserID: "u1"}, time.Hour)
	// A 64-byte signature is 86 base64url characters; the last one carries
	// two data bits and four bits that must be zero. Setting one of those
	// keeps the decoded bytes identical under lenient decoding.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, tok[len(tok)-1])
	variant := tok[:len(tok)-1] + string(alphabet[last|1])
	if variant == tok {
		variant = tok[:len(tok)-1] + string(alphabet[last|2])
	}
	if _, err := v.Verify(context.Background(), variant); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("non-canonical spelling accepted: %v", err)
	}
}
