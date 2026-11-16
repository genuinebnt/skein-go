package credential

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// testParams keep tests fast; the parameters are part of each hash, so the
// code paths are the same as with DefaultParams.
var testParams = Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func newTestHasher(t *testing.T, p Params) *Hasher {
	t.Helper()
	h, err := NewHasher(HasherOptions{Params: p})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Hashes produced by libargon2 (argon2-cffi 23, low_level.hash_secret) so
// this implementation is checked against the reference one, not itself.
var libargon2Vectors = []struct{ password, encoded string }{
	{"correct horse battery staple", "$argon2id$v=19$m=256,t=2,p=2$c2tlaW4ta2F0LXNhbHQtMQ$iT8ZX3iNP66UDO8mXJGzw49k7xIdGELl6eQUgNxWm7g"},
	{"密码パスワード🔑", "$argon2id$v=19$m=64,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ"},
}

func TestVerifyAcceptsHashesFromTheReferenceImplementation(t *testing.T) {
	h := newTestHasher(t, testParams)
	ctx := context.Background()
	for _, v := range libargon2Vectors {
		got, err := h.Verify(ctx, v.password, v.encoded)
		if err != nil || !got.Match {
			t.Errorf("%q: %+v %v", v.encoded, got, err)
		}
		if !got.NeedsRehash {
			t.Errorf("%q: parameters differ from the hasher's, so a rehash is due", v.encoded)
		}
		if bad, _ := h.Verify(ctx, v.password+"x", v.encoded); bad.Match {
			t.Errorf("%q matched a different password", v.encoded)
		}
		parsed, err := ParsePasswordHash(v.encoded)
		if err != nil || parsed.String() != v.encoded {
			t.Errorf("round trip of %q gave %q (%v)", v.encoded, parsed.String(), err)
		}
	}
}

func TestHashVerifiesAndOnlyForTheSamePassword(t *testing.T) {
	h := newTestHasher(t, testParams)
	ctx := context.Background()
	enc, err := h.Hash(ctx, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("unexpected encoding %q", enc)
	}
	if v, err := h.Verify(ctx, "correct horse battery staple", enc); err != nil || !v.Match || v.NeedsRehash {
		t.Fatalf("own hash: %+v %v", v, err)
	}
	if v, _ := h.Verify(ctx, "correct horse battery stapl", enc); v.Match {
		t.Fatal("matched a different password")
	}
	other, _ := h.Hash(ctx, "correct horse battery staple")
	if other == enc {
		t.Fatal("two hashes of one password share a salt")
	}
}

func TestChangedParametersAskForARehashInEitherDirection(t *testing.T) {
	ctx := context.Background()
	old := newTestHasher(t, testParams)
	enc, _ := old.Hash(ctx, "correct horse battery staple")
	for _, p := range []Params{
		{MemoryKiB: 128, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
		{MemoryKiB: 64, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32},
		{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16},
	} {
		v, err := newTestHasher(t, p).Verify(ctx, "correct horse battery staple", enc)
		if err != nil || !v.Match || !v.NeedsRehash {
			t.Errorf("params %+v: %+v %v", p, v, err)
		}
	}
}

func TestDefaultParamsAreTheDocumentedOnes(t *testing.T) {
	want := Params{MemoryKiB: 65536, Iterations: 3, Parallelism: 4, SaltLength: 16, KeyLength: 32}
	if DefaultParams != want {
		t.Fatalf("DefaultParams = %+v; the README documents %+v", DefaultParams, want)
	}
	if err := DefaultParams.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParserRejectsEverythingButCanonicalArgon2id(t *testing.T) {
	bad := []string{
		"",
		"$argon2i$v=19$m=64,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ",         // argon2i
		"$argon2id$v=16$m=64,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ",        // old version
		"$argon2id$v=19$t=1,m=64,p=1$MDEyMzQ1Njc4OWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ",        // order
		"$argon2id$v=19$m=064,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ",       // leading zero
		"$argon2id$v=19$m=64,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg==$ToufrQgmqqz9wT20buA7yQ",      // padding
		"$argon2id$v=19$m=64,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZh$ToufrQgmqqz9wT20buA7yQ",        // non-canonical trailing bits
		"$argon2id$v=19$m=999999999,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ", // memory bomb
		"$argon2id$v=19$m=64,t=99,p=1$MDEyMzQ1Njc4OWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ",
		"$argon2id$v=19$m=64,t=1,p=0$MDEyMzQ1Njc4OWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ",
		"$argon2id$v=19$m=64,t=1,p=1$c2FsdA$ToufrQgmqqz9wT20buA7yQ",                  // 4-byte salt
		"$argon2id$v=19$m=64,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$c2hvcnQ",                 // 5-byte key
		"$argon2id$v=19$m=64,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ$", // trailing field
		"$argon2id$v=19$m=64,t=1,p=1,k=1$MDEyMzQ1Njc4OWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ",
		"$argon2id$v=19$m=64,t=1,p=1$MDEyMzQ1Njc4\nOWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ", // newline the decoder would skip
	}
	for _, s := range bad {
		if _, err := ParsePasswordHash(s); !errors.Is(err, ErrMalformedHash) {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestDummyVerificationDoesTheSameWork(t *testing.T) {
	h := newTestHasher(t, testParams)
	if err := h.VerifyDummy(context.Background(), "anything"); err != nil {
		t.Fatal(err)
	}
	if h.dummy.Params != h.params {
		t.Fatal("the dummy hash must use the current parameters, or unknown accounts answer faster")
	}
}

func TestHashingWaitsForASlotAndHonoursCancellation(t *testing.T) {
	h, err := NewHasher(HasherOptions{Params: testParams, MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	h.slots <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "correct horse battery staple"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Hash with no free slot and a cancelled context: %v", err)
	}
}

func TestStrengthPolicy(t *testing.T) {
	for _, ok := range []string{"correct horse battery staple", "Tr0ub4dor&3xyz", "長いパスワード一二三四"} {
		if err := CheckStrength(ok, "ada@example.com"); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"short", "aaaaaaaaaaaa", "abababababab", "Password123", "1234567890", "adalovelace-secret", strings.Repeat("ab cd ", 200)} {
		if err := CheckStrength(bad, "adalovelace@example.com"); !errors.Is(err, ErrWeakPassword) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTokensHaveTheirKindsPrefixAndParseOnlyAsThatKind(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range []TokenKind{KindRefresh, KindShareLink, KindEmailVerification, KindPasswordReset} {
		tok, err := NewToken(k, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(tok, k.Prefix()) || len(tok) != len(k.Prefix())+43 {
			t.Errorf("%v token %q", k, tok)
		}
		if _, err := ParseToken(k, tok); err != nil {
			t.Errorf("own token refused: %v", err)
		}
		for _, other := range []TokenKind{KindRefresh, KindShareLink, KindEmailVerification, KindPasswordReset} {
			if other != k {
				if _, err := ParseToken(other, tok); err == nil {
					t.Errorf("%v token accepted as %v", k, other)
				}
			}
		}
		if seen[tok] {
			t.Error("duplicate token")
		}
		seen[tok] = true
	}
}

func TestTokenParserRejectsMalformedTokens(t *testing.T) {
	valid, _ := NewToken(KindRefresh, bytes.NewReader(bytes.Repeat([]byte{7}, 32)))
	bad := []string{"", "rt_", "rt_short", valid + "A", valid[:len(valid)-1], "RT_" + valid[3:],
		valid[:len(valid)-1] + "!", valid[:len(valid)-1] + "D" /* non-canonical trailing bits */}
	for _, s := range bad {
		if _, err := ParseToken(KindRefresh, s); !errors.Is(err, ErrMalformedToken) {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestHashTokenMatchesTheSeedScheme(t *testing.T) {
	// testdata/seed/small/share_links.jsonl, first line.
	if got := HashToken("shl_2eb8b0e6f9b335f782503db930d32958"); got != "8345f6b3900d5391e6473198670fa745e5b61f186f95eb77817d8605845c0f0c" {
		t.Fatalf("HashToken = %s", got)
	}
}

func FuzzParsePasswordHash(f *testing.F) {
	for _, v := range libargon2Vectors {
		f.Add(v.encoded)
	}
	f.Add("$argon2id$v=19$m=64,t=1,p=1$$")
	f.Add("$argon2id$v=19$m=18446744073709551616,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$ToufrQgmqqz9wT20buA7yQ")
	f.Fuzz(func(t *testing.T, s string) {
		h, err := ParsePasswordHash(s)
		if err != nil {
			if !errors.Is(err, ErrMalformedHash) {
				t.Fatalf("unexpected error type %v", err)
			}
			return
		}
		// Anything accepted is within bounds and has exactly one spelling.
		if err := h.Params.Validate(); err != nil {
			t.Fatalf("accepted out-of-bounds params: %v", err)
		}
		if h.String() != s {
			t.Fatalf("accepted non-canonical %q (canonical %q)", s, h.String())
		}
	})
}

func FuzzParseToken(f *testing.F) {
	tok, _ := NewToken(KindShareLink, bytes.NewReader(bytes.Repeat([]byte{1}, 32)))
	f.Add(tok)
	f.Add("sl_")
	f.Add("shl_2eb8b0e6f9b335f782503db930d32958")
	f.Fuzz(func(t *testing.T, s string) {
		got, err := ParseToken(KindShareLink, s)
		if err != nil {
			return
		}
		if got != s || !strings.HasPrefix(s, "sl_") || len(s) != 46 {
			t.Fatalf("accepted %q", s)
		}
		if _, err := ParseToken(KindRefresh, s); err == nil {
			t.Fatalf("share-link token %q also parses as a refresh token", s)
		}
	})
}
