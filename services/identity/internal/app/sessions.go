package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// SessionTokens are what a successful sign-in returns. RefreshToken is the
// raw secret; only its hash is stored.
type SessionTokens struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
}

// SignUp creates an account with a password and signs it in.
func (s *Service) SignUp(ctx context.Context, email, password, displayName string) (domain.User, SessionTokens, error) {
	email, err := domain.NormalizeEmail(email)
	if err != nil {
		return domain.User{}, SessionTokens{}, err
	}
	name, err := domain.NormalizeName("display_name", displayName, domain.MaxDisplayNameRunes)
	if err != nil {
		return domain.User{}, SessionTokens{}, err
	}
	if err := credential.CheckStrength(password, email); err != nil {
		return domain.User{}, SessionTokens{}, domain.InvalidField("password", err.Error())
	}
	// Hash before the transaction: it takes tens of milliseconds and must
	// not hold a connection or locks.
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return domain.User{}, SessionTokens{}, err
	}
	id, err := domain.NewID()
	if err != nil {
		return domain.User{}, SessionTokens{}, err
	}
	now := s.now()
	user := domain.User{ID: domain.UserID(id), Email: email, DisplayName: name, PasswordHash: hash, CreatedAt: now}
	var tokens SessionTokens
	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		if err := tx.InsertUser(ctx, user); err != nil {
			return err
		}
		tokens, err = s.startSession(ctx, tx, user.ID, now)
		return err
	})
	if err != nil {
		return domain.User{}, SessionTokens{}, err
	}
	return user, tokens, nil
}

// startSession creates a session with its first refresh token and an
// access token, inside tx.
func (s *Service) startSession(ctx context.Context, tx Tx, user domain.UserID, now time.Time) (SessionTokens, error) {
	id, err := domain.NewID()
	if err != nil {
		return SessionTokens{}, err
	}
	sess := domain.Session{ID: domain.SessionID(id), UserID: user, CreatedAt: now}
	if err := tx.InsertSession(ctx, sess); err != nil {
		return SessionTokens{}, err
	}
	return s.issueRefresh(ctx, tx, sess, now)
}

// issueRefresh adds a refresh token to sess's chain and signs an access
// token for its user.
func (s *Service) issueRefresh(ctx context.Context, tx Tx, sess domain.Session, now time.Time) (SessionTokens, error) {
	raw, err := credential.NewToken(credential.KindRefresh, s.random)
	if err != nil {
		return SessionTokens{}, err
	}
	rt := domain.RefreshToken{
		Hash: credential.HashToken(raw), SessionID: sess.ID,
		IssuedAt: now, ExpiresAt: now.Add(s.cfg.RefreshTokenTTL),
	}
	if err := tx.InsertRefreshToken(ctx, rt); err != nil {
		return SessionTokens{}, err
	}
	access, accessExp, err := s.issuer.Issue(domain.UserSubject(sess.UserID, ""), s.cfg.AccessTokenTTL)
	if err != nil {
		return SessionTokens{}, err
	}
	return SessionTokens{AccessToken: access, AccessExpiresAt: accessExp, RefreshToken: raw, RefreshExpiresAt: rt.ExpiresAt}, nil
}

// ErrDevSessionsDisabled means StartDevSession was called without
// Config.DevSessions.
var ErrDevSessionsDisabled = errors.New("identity: dev sessions are disabled")

// StartDevSession starts a session for an existing user without any
// credential. It exists only so tests against a local stack can act as seed
// users, who have no passwords; it fails with ErrDevSessionsDisabled unless
// Config.DevSessions is set, and domain.ErrNotFound for an unknown user.
// The session is an ordinary one: it refreshes, rotates and signs out like
// any other.
func (s *Service) StartDevSession(ctx context.Context, userID string) (domain.User, SessionTokens, error) {
	if !s.cfg.DevSessions {
		return domain.User{}, SessionTokens{}, ErrDevSessionsDisabled
	}
	var user domain.User
	var tokens SessionTokens
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		if user, err = tx.UserByID(ctx, domain.UserID(userID)); err != nil {
			return err
		}
		tokens, err = s.startSession(ctx, tx, user.ID, s.now())
		return err
	})
	if err != nil {
		return domain.User{}, SessionTokens{}, err
	}
	s.log.WarnContext(ctx, "dev session started", "user_id", userID)
	return user, tokens, nil
}

// SignIn checks an email and password and starts a session. Every failure
// is ErrUnauthenticated with the same message, and the work done is the
// same whether or not the account exists.
func (s *Service) SignIn(ctx context.Context, email, password, clientIP string) (domain.User, SessionTokens, error) {
	normalized, emailErr := domain.NormalizeEmail(email)
	if err := s.checkSignInLimits(ctx, normalized, clientIP); err != nil {
		return domain.User{}, SessionTokens{}, err
	}
	var user domain.User
	if emailErr == nil {
		err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
			var err error
			user, err = tx.UserByEmail(ctx, normalized)
			return err
		})
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return domain.User{}, SessionTokens{}, err
		}
	}
	if user.PasswordHash == "" {
		if err := s.hasher.VerifyDummy(ctx, password); err != nil {
			return domain.User{}, SessionTokens{}, err
		}
		return domain.User{}, SessionTokens{}, domain.ErrUnauthenticated
	}
	v, err := s.hasher.Verify(ctx, password, user.PasswordHash)
	if err != nil {
		// A stored hash we cannot parse is a data problem, not a wrong
		// password; surface it so it gets fixed.
		return domain.User{}, SessionTokens{}, fmt.Errorf("verify password of %s: %w", user.ID, err)
	}
	if !v.Match {
		return domain.User{}, SessionTokens{}, domain.ErrUnauthenticated
	}
	var rehash string
	if v.NeedsRehash {
		if rehash, err = s.hasher.Hash(ctx, password); err != nil {
			return domain.User{}, SessionTokens{}, err
		}
	}
	now := s.now()
	var tokens SessionTokens
	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		if rehash != "" {
			if err := tx.SetPasswordHash(ctx, user.ID, rehash); err != nil {
				return err
			}
		}
		tokens, err = s.startSession(ctx, tx, user.ID, now)
		return err
	})
	if err != nil {
		return domain.User{}, SessionTokens{}, err
	}
	if rehash != "" {
		user.PasswordHash = rehash
		s.log.InfoContext(ctx, "password rehashed with current parameters", "user_id", string(user.ID))
	}
	return user, tokens, nil
}

// checkSignInLimits applies the per-account and per-IP limits. The account
// key is a hash of the email so Redis keys never hold addresses.
func (s *Service) checkSignInLimits(ctx context.Context, email, clientIP string) error {
	if s.limiter == nil {
		return nil
	}
	type check struct {
		subject, key string
		limit        ratelimit.Limit
	}
	var checks []check
	if email != "" {
		sum := sha256.Sum256([]byte(email))
		checks = append(checks, check{"user", "identity:signin:account:" + hex.EncodeToString(sum[:16]), s.cfg.SignInPerAccount})
	}
	if clientIP != "" {
		checks = append(checks, check{"ip", "identity:signin:ip:" + clientIP, s.cfg.SignInPerIP})
	}
	var worst time.Duration
	for _, c := range checks {
		d, err := s.limiter.Allow(ctx, c.key, c.limit, 1)
		if err != nil {
			s.log.WarnContext(ctx, "sign-in rate limiter failed; allowing", "error", err)
			continue
		}
		decision := "allowed"
		if !d.Allowed {
			decision = "limited"
			worst = max(worst, d.RetryAfter)
		}
		s.metrics.rateLimit.Add(ctx, 1, metric.WithAttributes(
			attribute.String("skein.ratelimit.layer", "local"), attribute.String("skein.ratelimit.budget", "api"),
			attribute.String("skein.ratelimit.subject", c.subject), attribute.String("skein.ratelimit.decision", decision)))
		if !d.Allowed && worst == 0 {
			worst = time.Second
		}
	}
	if worst > 0 {
		return &RateLimitError{RetryAfter: worst}
	}
	return nil
}

// ValidCodeVerifier reports whether v is an RFC 7636 code verifier: 43 to
// 128 characters of [A-Za-z0-9-._~].
func ValidCodeVerifier(v string) bool {
	if len(v) < 43 || len(v) > 128 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~'
		if !ok {
			return false
		}
	}
	return true
}

// OAuthResult is the outcome of SignInWithOAuth.
type OAuthResult struct {
	User    domain.User
	Tokens  SessionTokens
	Created bool
}

// SignInWithOAuth completes an authorization-code flow with PKCE.
//
// Account matching: an existing link (provider, subject) wins. Otherwise
// the provider must vouch for the email; an account with that email is
// linked, and a new one is created if there is none. When the matched
// account's email was never verified, whoever set its password never proved
// they own the address, so the password is removed and its sessions are
// revoked before linking: this defeats "pre-hijacking", where an attacker
// signs up with the victim's email first and waits for them to arrive
// through OAuth.
func (s *Service) SignInWithOAuth(ctx context.Context, kind OAuthProviderKind, code, verifier, redirectURI string) (OAuthResult, error) {
	provider, ok := s.oauth[kind]
	if !ok {
		return OAuthResult{}, domain.InvalidField("provider", "is not configured")
	}
	if code == "" {
		return OAuthResult{}, domain.InvalidField("code", "is required")
	}
	if !ValidCodeVerifier(verifier) {
		return OAuthResult{}, domain.InvalidField("code_verifier", "must be 43 to 128 characters of [A-Za-z0-9-._~] (RFC 7636)")
	}
	if !slices.Contains(s.cfg.OAuthRedirectURIs, redirectURI) {
		return OAuthResult{}, domain.InvalidField("redirect_uri", "is not an allowed redirect URI")
	}
	ext, err := provider.Exchange(ctx, code, verifier, redirectURI)
	if err != nil {
		return OAuthResult{}, err
	}
	if ext.Subject == "" {
		return OAuthResult{}, fmt.Errorf("%s: provider returned no subject: %w", kind, domain.ErrUnauthenticated)
	}
	now := s.now()
	var res OAuthResult
	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		user, created, err := s.oauthAccount(ctx, tx, kind, ext, now)
		if err != nil {
			return err
		}
		tokens, err := s.startSession(ctx, tx, user.ID, now)
		res = OAuthResult{User: user, Tokens: tokens, Created: created}
		return err
	})
	return res, err
}

func (s *Service) oauthAccount(ctx context.Context, tx Tx, kind OAuthProviderKind, ext ExternalIdentity, now time.Time) (domain.User, bool, error) {
	link, err := tx.OAuthIdentity(ctx, kind, ext.Subject)
	if err == nil {
		u, err := tx.UserByID(ctx, link.UserID)
		return u, false, err
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.User{}, false, err
	}
	if !ext.EmailVerified {
		return domain.User{}, false, fmt.Errorf("%w: the provider has not verified this email address", domain.ErrFailedPrecondition)
	}
	email, err := domain.NormalizeEmail(ext.Email)
	if err != nil {
		return domain.User{}, false, err
	}
	user, err := tx.UserByEmail(ctx, email)
	created := false
	switch {
	case errors.Is(err, domain.ErrNotFound):
		id, err := domain.NewID()
		if err != nil {
			return domain.User{}, false, err
		}
		name, nameErr := domain.NormalizeName("display_name", ext.DisplayName, domain.MaxDisplayNameRunes)
		if nameErr != nil {
			// Fall back to the local part of the address.
			name, _, _ = strings.Cut(email, "@")
		}
		user = domain.User{ID: domain.UserID(id), Email: email, DisplayName: name, EmailVerifiedAt: &now, CreatedAt: now}
		if err := tx.InsertUser(ctx, user); err != nil {
			return domain.User{}, false, err
		}
		created = true
	case err != nil:
		return domain.User{}, false, err
	case user.EmailVerifiedAt == nil:
		if user.PasswordHash != "" {
			if err := tx.SetPasswordHash(ctx, user.ID, ""); err != nil {
				return domain.User{}, false, err
			}
			user.PasswordHash = ""
		}
		if _, err := tx.RevokeUserSessions(ctx, user.ID, now, domain.RevokeAccountTaken); err != nil {
			return domain.User{}, false, err
		}
		if err := tx.MarkEmailVerified(ctx, user.ID, now); err != nil {
			return domain.User{}, false, err
		}
		user.EmailVerifiedAt = &now
	}
	if err := tx.InsertOAuthIdentity(ctx, OAuthIdentity{Provider: kind, Subject: ext.Subject, UserID: user.ID, CreatedAt: now}); err != nil {
		return domain.User{}, false, err
	}
	return user, created, nil
}

// Refresh exchanges a refresh token for a new one and a new access token.
// Presenting a token that was already exchanged revokes its session: the
// revocation commits, then the caller gets ErrUnauthenticated.
func (s *Service) Refresh(ctx context.Context, raw string) (SessionTokens, error) {
	token, err := credential.ParseToken(credential.KindRefresh, raw)
	if err != nil {
		return SessionTokens{}, domain.ErrUnauthenticated
	}
	hash := credential.HashToken(token)
	now := s.now()
	var tokens SessionTokens
	var outcome domain.RefreshOutcome
	var sess domain.Session
	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		rt, err := tx.RefreshTokenForUpdate(ctx, hash)
		if err != nil {
			return err
		}
		if sess, err = tx.SessionByIDForUpdate(ctx, rt.SessionID); err != nil {
			return err
		}
		outcome = domain.DecideRefresh(sess, rt, now)
		switch outcome {
		case domain.RefreshRotate:
			if err := tx.MarkRefreshTokenRotated(ctx, hash, now); err != nil {
				return err
			}
			tokens, err = s.issueRefresh(ctx, tx, sess, now)
			return err
		case domain.RefreshReuseDetected:
			return tx.RevokeSession(ctx, sess.ID, now, domain.RevokeTokenReuse)
		}
		return nil
	})
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return SessionTokens{}, domain.ErrUnauthenticated
	case err != nil:
		return SessionTokens{}, err
	case outcome == domain.RefreshReuseDetected:
		s.log.WarnContext(ctx, "refresh token reuse detected; session revoked",
			"user_id", string(sess.UserID), "session_id", string(sess.ID))
		return SessionTokens{}, domain.ErrUnauthenticated
	case outcome != domain.RefreshRotate:
		return SessionTokens{}, domain.ErrUnauthenticated
	}
	return tokens, nil
}

// SignOut revokes the session that owns the refresh token, whichever token
// of the chain is presented. Signing out a revoked session succeeds.
func (s *Service) SignOut(ctx context.Context, raw string) error {
	token, err := credential.ParseToken(credential.KindRefresh, raw)
	if err != nil {
		return domain.ErrUnauthenticated
	}
	hash := credential.HashToken(token)
	now := s.now()
	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		rt, err := tx.RefreshTokenForUpdate(ctx, hash)
		if err != nil {
			return err
		}
		sess, err := tx.SessionByIDForUpdate(ctx, rt.SessionID)
		if err != nil || sess.RevokedAt != nil {
			return err
		}
		return tx.RevokeSession(ctx, sess.ID, now, domain.RevokeSignOut)
	})
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrUnauthenticated
	}
	return err
}

// RequestEmailVerification mints a verification token for the user and
// returns it for delivery by email. There is no RPC for it yet (see the
// service README); the use case is complete so only the transport is
// missing.
func (s *Service) RequestEmailVerification(ctx context.Context, user domain.UserID) (string, error) {
	return s.mintOneTime(ctx, func(ctx context.Context, tx Tx) (domain.UserID, error) {
		u, err := tx.UserByID(ctx, user)
		if err != nil {
			return "", err
		}
		if u.EmailVerifiedAt != nil {
			return "", fmt.Errorf("%w: email already verified", domain.ErrFailedPrecondition)
		}
		return u.ID, nil
	}, credential.KindEmailVerification, domain.PurposeEmailVerification, domain.EmailVerificationTTL)
}

// VerifyEmail redeems a verification token.
func (s *Service) VerifyEmail(ctx context.Context, raw string) error {
	now := s.now()
	return s.redeem(ctx, raw, credential.KindEmailVerification, domain.PurposeEmailVerification,
		func(ctx context.Context, tx Tx, user domain.UserID) error {
			return tx.MarkEmailVerified(ctx, user, now)
		})
}

// RequestPasswordReset mints a reset token for the account with this email.
// found is false for an unknown email; the eventual RPC must answer the
// same either way, so that it cannot be used to probe for accounts.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) (token string, found bool, err error) {
	normalized, err := domain.NormalizeEmail(email)
	if err != nil {
		return "", false, err
	}
	token, err = s.mintOneTime(ctx, func(ctx context.Context, tx Tx) (domain.UserID, error) {
		u, err := tx.UserByEmail(ctx, normalized)
		return u.ID, err
	}, credential.KindPasswordReset, domain.PurposePasswordReset, domain.PasswordResetTTL)
	if errors.Is(err, domain.ErrNotFound) {
		return "", false, nil
	}
	return token, err == nil, err
}

// ResetPassword redeems a reset token: it sets the new password, marks the
// email verified (the reset link proved the inbox) and revokes every
// session, since whoever knew the old password may hold one.
func (s *Service) ResetPassword(ctx context.Context, raw, newPassword string) error {
	if len(newPassword) > credential.MaxPasswordBytes {
		return domain.InvalidField("password", "is too long")
	}
	// The strength check needs the email, so peek at the token's user
	// first; the redemption below re-checks everything under a lock.
	hashOf := credential.HashToken(raw)
	var email string
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		t, err := tx.OneTimeTokenForUpdate(ctx, hashOf)
		if err != nil {
			return err
		}
		u, err := tx.UserByID(ctx, t.UserID)
		email = u.Email
		return err
	})
	if err != nil {
		return domain.ErrUnauthenticated
	}
	if err := credential.CheckStrength(newPassword, email); err != nil {
		return domain.InvalidField("password", err.Error())
	}
	hash, err := s.hasher.Hash(ctx, newPassword)
	if err != nil {
		return err
	}
	now := s.now()
	return s.redeem(ctx, raw, credential.KindPasswordReset, domain.PurposePasswordReset,
		func(ctx context.Context, tx Tx, user domain.UserID) error {
			if err := tx.SetPasswordHash(ctx, user, hash); err != nil {
				return err
			}
			if err := tx.MarkEmailVerified(ctx, user, now); err != nil {
				return err
			}
			_, err := tx.RevokeUserSessions(ctx, user, now, domain.RevokePasswordReset)
			return err
		})
}

func (s *Service) mintOneTime(ctx context.Context, owner func(context.Context, Tx) (domain.UserID, error),
	kind credential.TokenKind, purpose domain.TokenPurpose, ttl time.Duration) (string, error) {
	raw, err := credential.NewToken(kind, s.random)
	if err != nil {
		return "", err
	}
	now := s.now()
	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		user, err := owner(ctx, tx)
		if err != nil {
			return err
		}
		return tx.InsertOneTimeToken(ctx, domain.OneTimeToken{
			Hash: credential.HashToken(raw), UserID: user, Purpose: purpose, CreatedAt: now, ExpiresAt: now.Add(ttl),
		})
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}

func (s *Service) redeem(ctx context.Context, raw string, kind credential.TokenKind, purpose domain.TokenPurpose,
	effect func(context.Context, Tx, domain.UserID) error) error {
	token, err := credential.ParseToken(kind, raw)
	if err != nil {
		return domain.ErrUnauthenticated
	}
	hash := credential.HashToken(token)
	now := s.now()
	err = s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		t, err := tx.OneTimeTokenForUpdate(ctx, hash)
		if err != nil {
			return err
		}
		if err := domain.DecideRedeem(t, purpose, now); err != nil {
			return err
		}
		if err := tx.MarkOneTimeTokenUsed(ctx, hash, now); err != nil {
			return err
		}
		return effect(ctx, tx, t.UserID)
	})
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrUnauthenticated
	}
	return err
}
