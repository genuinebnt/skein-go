package app_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
	"github.com/genuinebnt/skein-go/services/identity/internal/fakes"
)

func TestSignUpThenSignInIssuesASessionForTheUser(t *testing.T) {
	h := newHarness(t)
	u, s := h.signUp(" Ada@Example.com ")
	if u.Email != "ada@example.com" {
		t.Errorf("email stored as %q", u.Email)
	}
	if !strings.HasPrefix(s.RefreshToken, "rt_") || !s.AccessExpiresAt.Equal(h.clock.Now().Add(15*time.Minute)) ||
		!s.RefreshExpiresAt.Equal(h.clock.Now().Add(30*24*time.Hour)) {
		t.Errorf("session %+v", s)
	}
	if issued, _ := h.issuer.Lookup(s.AccessToken); issued.Subject.UserID != u.ID || issued.Subject.AgentRunID != "" {
		t.Errorf("access token for %+v", issued.Subject)
	}
	got, s2, err := h.svc.SignIn(ctx, "ADA@example.com", password, "10.0.0.1")
	if err != nil || got.ID != u.ID || s2.RefreshToken == s.RefreshToken {
		t.Fatalf("sign in: %+v %v", got, err)
	}
	for _, rt := range h.store.RefreshTokens() {
		if rt.Hash == s.RefreshToken || rt.Hash == s2.RefreshToken {
			t.Fatal("a raw refresh token was stored")
		}
	}
}

func TestSignUpRejectsTakenEmailsAndWeakPasswords(t *testing.T) {
	h := newHarness(t)
	h.signUp("ada@example.com")
	_, _, err := h.svc.SignUp(ctx, "ADA@example.com", password, "Other Ada")
	wantErr(t, err, domain.ErrAlreadyExists)
	_, _, err = h.svc.SignUp(ctx, "grace@example.com", "short", "Grace")
	wantErr(t, err, domain.ErrInvalidArgument)
	if fieldOf(err) != "password" {
		t.Errorf("weak password error names %q", fieldOf(err))
	}
	notAnEmail := "not-an-email"
	_, _, err = h.svc.SignUp(ctx, notAnEmail, password, "Grace")
	if fieldOf(err) != "email" {
		t.Errorf("bad email error: %v", err)
	}
	_, _, err = h.svc.SignUp(ctx, "grace@example.com", password, "  ")
	if fieldOf(err) != "display_name" {
		t.Errorf("blank name error: %v", err)
	}
}

func TestSignInFailuresLookTheSame(t *testing.T) {
	h := newHarness(t)
	h.signUp("ada@example.com")
	_, _, wrong := h.svc.SignIn(ctx, "ada@example.com", wrongPassword, "10.0.0.1")
	unknownEmail := "nobody@example.com"
	_, _, unknown := h.svc.SignIn(ctx, unknownEmail, password, "10.0.0.2")
	malformedEmail := strings.TrimSuffix(unknownEmail, "@example.com") // no @, so not an address
	_, _, malformed := h.svc.SignIn(ctx, malformedEmail, password, "10.0.0.3")
	for _, err := range []error{wrong, unknown, malformed} {
		wantErr(t, err, domain.ErrUnauthenticated)
		if err.Error() != wrong.Error() {
			t.Errorf("messages differ: %q vs %q", err, wrong)
		}
	}
}

func TestSignInRehashesWhenTheParametersChange(t *testing.T) {
	h := newHarness(t)
	h.signUp("ada@example.com")
	stronger := credential.Params{MemoryKiB: 128, Iterations: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	h.svc = h.service(stronger)
	u, _, err := h.svc.SignIn(ctx, "ada@example.com", password, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$v=19$m=128,t=2,p=1$") {
		t.Fatalf("hash not upgraded: %s", u.PasswordHash)
	}
	// The stored hash changed too, and still verifies.
	stored, _, err := h.svc.SignIn(ctx, "ada@example.com", password, "")
	if err != nil || stored.PasswordHash != u.PasswordHash {
		t.Fatalf("second sign-in: %v, hash %s", err, stored.PasswordHash)
	}
}

func TestSignInIsRateLimitedPerAccountAndPerIP(t *testing.T) {
	h := newHarness(t)
	h.signUp("ada@example.com")
	h.signUp("grace@example.com")
	burst := int(h.cfg.SignInPerAccount.Burst)
	for i := 0; i < burst; i++ {
		_, _, err := h.svc.SignIn(ctx, "ada@example.com", wrongPassword, "10.0.0."+string(rune('a'+i)))
		wantErr(t, err, domain.ErrUnauthenticated)
	}
	_, _, err := h.svc.SignIn(ctx, "ada@example.com", password, "10.9.9.9")
	var rl *app.RateLimitError
	if !errors.As(err, &rl) || rl.RetryAfter <= 0 {
		t.Fatalf("attempt %d on one account: %v, want a rate limit with RetryAfter", burst+1, err)
	}
	if _, _, err := h.svc.SignIn(ctx, "grace@example.com", password, "10.9.9.9"); err != nil {
		t.Fatalf("another account is limited too: %v", err)
	}
	h.clock.Advance(time.Minute)
	if _, _, err := h.svc.SignIn(ctx, "ada@example.com", password, "10.9.9.9"); err != nil {
		t.Fatalf("limit did not refill: %v", err)
	}
	// Per IP: many accounts from one address.
	ipBurst := int(h.cfg.SignInPerIP.Burst)
	for i := 0; i < ipBurst; i++ {
		_, _, _ = h.svc.SignIn(ctx, "user"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@example.com", password, "192.0.2.1")
	}
	_, _, err = h.svc.SignIn(ctx, "grace@example.com", password, "192.0.2.1")
	if !errors.As(err, &rl) {
		t.Fatalf("IP over its limit: %v", err)
	}
}

func TestRefreshRotatesAndReuseRevokesTheWholeSession(t *testing.T) {
	h := newHarness(t)
	_, first := h.signUp("ada@example.com")
	second, err := h.svc.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh did not rotate")
	}
	third, err := h.svc.Refresh(ctx, second.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	// The thief replays the first token.
	_, err = h.svc.Refresh(ctx, first.RefreshToken)
	wantErr(t, err, domain.ErrUnauthenticated)
	// The newest token of the same family is dead too.
	_, err = h.svc.Refresh(ctx, third.RefreshToken)
	wantErr(t, err, domain.ErrUnauthenticated)
	var revoked int
	for _, rt := range h.store.RefreshTokens() {
		s, _ := h.store.Session(rt.SessionID)
		if s.RevokedAt != nil && s.RevokeReason == domain.RevokeTokenReuse {
			revoked++
		}
	}
	if revoked != 3 {
		t.Fatalf("%d tokens in a reuse-revoked session, want all 3", revoked)
	}
	// Other sessions of the same user are untouched.
	_, other, err := h.svc.SignIn(ctx, "ada@example.com", password, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Refresh(ctx, other.RefreshToken); err != nil {
		t.Fatalf("an unrelated session was revoked: %v", err)
	}
}

func TestConcurrentRefreshesOfOneTokenLetOnlyOneWin(t *testing.T) {
	h := newHarness(t)
	_, s := h.signUp("ada@example.com")
	const n = 8
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = h.svc.Refresh(ctx, s.RefreshToken)
		}()
	}
	wg.Wait()
	ok := 0
	for _, err := range results {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d of %d concurrent refreshes succeeded, want exactly 1", ok, n)
	}
}

func TestExpiredAndMalformedRefreshTokensAreRefused(t *testing.T) {
	h := newHarness(t)
	_, s := h.signUp("ada@example.com")
	for _, bad := range []string{"", "rt_nope", "sl_" + s.RefreshToken[3:], s.RefreshToken + "x"} {
		_, err := h.svc.Refresh(ctx, bad)
		wantErr(t, err, domain.ErrUnauthenticated)
	}
	h.clock.Advance(30 * 24 * time.Hour)
	_, err := h.svc.Refresh(ctx, s.RefreshToken)
	wantErr(t, err, domain.ErrUnauthenticated)
}

func TestSignOutRevokesTheSessionAndIsIdempotent(t *testing.T) {
	h := newHarness(t)
	_, s := h.signUp("ada@example.com")
	rotated, _ := h.svc.Refresh(ctx, s.RefreshToken)
	if err := h.svc.SignOut(ctx, rotated.RefreshToken); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.Refresh(ctx, rotated.RefreshToken)
	wantErr(t, err, domain.ErrUnauthenticated)
	if err := h.svc.SignOut(ctx, rotated.RefreshToken); err != nil {
		t.Fatalf("second sign-out: %v", err)
	}
	// An older token of the chain also identifies the session.
	_, s2, _ := h.svc.SignIn(ctx, "ada@example.com", password, "")
	newer, _ := h.svc.Refresh(ctx, s2.RefreshToken)
	if err := h.svc.SignOut(ctx, s2.RefreshToken); err != nil {
		t.Fatal(err)
	}
	_, err = h.svc.Refresh(ctx, newer.RefreshToken)
	wantErr(t, err, domain.ErrUnauthenticated)
	wantErr(t, h.svc.SignOut(ctx, "rt_unknown"), domain.ErrUnauthenticated)
}

func grant(h *harness, code string, id app.ExternalIdentity) {
	h.google.Grant(code, fakes.OAuthGrant{Challenge: fakes.S256(verifier), RedirectURI: redirectURI, Identity: id})
}

func TestOAuthCreatesAnAccountOnceAndThenSignsIntoIt(t *testing.T) {
	h := newHarness(t)
	ada := app.ExternalIdentity{Subject: "google-123", Email: "Ada@Example.com", EmailVerified: true, DisplayName: "Ada Lovelace"}
	grant(h, "code-1", ada)
	first, err := h.svc.SignInWithOAuth(ctx, app.OAuthGoogle, "code-1", verifier, redirectURI)
	if err != nil || !first.Created || first.User.Email != "ada@example.com" || first.User.EmailVerifiedAt == nil {
		t.Fatalf("first OAuth sign-in: %+v %v", first, err)
	}
	h.check()
	grant(h, "code-2", ada)
	second, err := h.svc.SignInWithOAuth(ctx, app.OAuthGoogle, "code-2", verifier, redirectURI)
	if err != nil || second.Created || second.User.ID != first.User.ID {
		t.Fatalf("second OAuth sign-in: %+v %v", second, err)
	}
	// A code works once.
	_, err = h.svc.SignInWithOAuth(ctx, app.OAuthGoogle, "code-2", verifier, redirectURI)
	wantErr(t, err, domain.ErrUnauthenticated)
}

func TestOAuthChecksPKCERedirectAndProvider(t *testing.T) {
	h := newHarness(t)
	id := app.ExternalIdentity{Subject: "s", Email: "ada@example.com", EmailVerified: true}
	grant(h, "code", id)
	other := "Ab" + verifier[2:]
	_, err := h.svc.SignInWithOAuth(ctx, app.OAuthGoogle, "code", other, redirectURI)
	wantErr(t, err, domain.ErrUnauthenticated)
	for _, c := range []struct {
		provider            app.OAuthProviderKind
		code, verifier, uri string
		field               string
	}{
		{app.OAuthGitHub, "code", verifier, redirectURI, "provider"},
		{app.OAuthGoogle, "", verifier, redirectURI, "code"},
		{app.OAuthGoogle, "code", "too-short", redirectURI, "code_verifier"},
		{app.OAuthGoogle, "code", verifier + "!", redirectURI, "code_verifier"},
		{app.OAuthGoogle, "code", verifier, "https://evil.example/callback", "redirect_uri"},
	} {
		_, err := h.svc.SignInWithOAuth(ctx, c.provider, c.code, c.verifier, c.uri)
		if fieldOf(err) != c.field {
			t.Errorf("%+v: %v", c, err)
		}
	}
	grant(h, "unverified", app.ExternalIdentity{Subject: "s2", Email: "x@example.com", EmailVerified: false})
	_, err = h.svc.SignInWithOAuth(ctx, app.OAuthGoogle, "unverified", verifier, redirectURI)
	wantErr(t, err, domain.ErrFailedPrecondition)
}

// TestOAuthClaimsAnUnverifiedAccountAndLocksThePreHijackerOut: an attacker
// signs up with the victim's address and a password of their choosing; the
// victim later arrives through Google. The victim gets the account; the
// attacker's password and sessions stop working.
func TestOAuthClaimsAnUnverifiedAccountAndLocksThePreHijackerOut(t *testing.T) {
	h := newHarness(t)
	attacker, attackerSession := h.signUp("victim@example.com")
	grant(h, "code", app.ExternalIdentity{Subject: "g-victim", Email: "victim@example.com", EmailVerified: true})
	res, err := h.svc.SignInWithOAuth(ctx, app.OAuthGoogle, "code", verifier, redirectURI)
	if err != nil || res.Created || res.User.ID != attacker.ID || res.User.PasswordHash != "" {
		t.Fatalf("%+v %v", res, err)
	}
	_, _, err = h.svc.SignIn(ctx, "victim@example.com", password, "")
	wantErr(t, err, domain.ErrUnauthenticated)
	_, err = h.svc.Refresh(ctx, attackerSession.RefreshToken)
	wantErr(t, err, domain.ErrUnauthenticated)
}

func TestOAuthLinksAVerifiedAccountWithoutTouchingIt(t *testing.T) {
	h := newHarness(t)
	u, s := h.signUp("ada@example.com")
	tok, _ := h.svc.RequestEmailVerification(ctx, u.ID)
	if err := h.svc.VerifyEmail(ctx, tok); err != nil {
		t.Fatal(err)
	}
	grant(h, "code", app.ExternalIdentity{Subject: "g-ada", Email: "ada@example.com", EmailVerified: true})
	if _, err := h.svc.SignInWithOAuth(ctx, app.OAuthGoogle, "code", verifier, redirectURI); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.SignIn(ctx, "ada@example.com", password, ""); err != nil {
		t.Fatalf("password stopped working: %v", err)
	}
	if _, err := h.svc.Refresh(ctx, s.RefreshToken); err != nil {
		t.Fatalf("existing session revoked: %v", err)
	}
}

func TestEmailVerificationTokensWorkOnce(t *testing.T) {
	h := newHarness(t)
	u, _ := h.signUp("ada@example.com")
	tok, err := h.svc.RequestEmailVerification(ctx, u.ID)
	if err != nil || !strings.HasPrefix(tok, "ev_") {
		t.Fatalf("%q %v", tok, err)
	}
	wantErr(t, h.svc.ResetPassword(ctx, tok, "another good password"), domain.ErrUnauthenticated)
	if err := h.svc.VerifyEmail(ctx, tok); err != nil {
		t.Fatal(err)
	}
	wantErr(t, h.svc.VerifyEmail(ctx, tok), domain.ErrUnauthenticated)
	_, err = h.svc.RequestEmailVerification(ctx, u.ID)
	wantErr(t, err, domain.ErrFailedPrecondition)
}

func TestPasswordResetSetsThePasswordAndEndsEverySession(t *testing.T) {
	h := newHarness(t)
	_, s := h.signUp("ada@example.com")
	tok, found, err := h.svc.RequestPasswordReset(ctx, "ADA@example.com")
	if err != nil || !found || !strings.HasPrefix(tok, "pr_") {
		t.Fatalf("%q %v %v", tok, found, err)
	}
	wantErr(t, h.svc.ResetPassword(ctx, tok, "short"), domain.ErrInvalidArgument)
	if err := h.svc.ResetPassword(ctx, tok, "a brand new passphrase"); err != nil {
		t.Fatal(err)
	}
	_, err = h.svc.Refresh(ctx, s.RefreshToken)
	wantErr(t, err, domain.ErrUnauthenticated)
	_, _, err = h.svc.SignIn(ctx, "ada@example.com", password, "")
	wantErr(t, err, domain.ErrUnauthenticated)
	if _, _, err := h.svc.SignIn(ctx, "ada@example.com", "a brand new passphrase", ""); err != nil {
		t.Fatal(err)
	}
	wantErr(t, h.svc.ResetPassword(ctx, tok, "yet another passphrase"), domain.ErrUnauthenticated)
	if _, found, err := h.svc.RequestPasswordReset(ctx, "nobody@example.com"); found || err != nil {
		t.Fatalf("unknown email: %v %v", found, err)
	}
	expiring, _, _ := h.svc.RequestPasswordReset(ctx, "ada@example.com")
	h.clock.Advance(time.Hour)
	wantErr(t, h.svc.ResetPassword(ctx, expiring, "an expired passphrase"), domain.ErrUnauthenticated)
}
