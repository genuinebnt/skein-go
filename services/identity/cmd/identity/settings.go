package main

import (
	"fmt"
	"time"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/config"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/keys"
)

// settings is identity's configuration beyond what platform loads. Every
// variable is listed in services/identity/README.md.
type settings struct {
	Service           config.Service
	GRPCAddr          string
	AdminAddr         string
	App               app.Config
	Argon2            credential.Params
	Argon2Concurrency int
	SigningKeys       []keys.ConfiguredKey
	KeyRotation       time.Duration
	Google, GitHub    oauthClient
	FixedNow          time.Time
}

type oauthClient struct {
	ID     string
	Secret config.Secret
}

// loadSettings reads the environment through l; l.Err() reports every
// problem at once.
func loadSettings(l *config.Loader) settings {
	s := settings{Service: config.LoadService(l, "identity")}
	s.GRPCAddr = l.String("IDENTITY_GRPC_ADDR", ":9090")
	s.AdminAddr = l.String("IDENTITY_ADMIN_ADDR", ":8080")

	s.App = app.DefaultConfig()
	s.App.AccessTokenTTL = l.Duration("IDENTITY_ACCESS_TOKEN_TTL", s.App.AccessTokenTTL)
	s.App.RefreshTokenTTL = l.Duration("IDENTITY_REFRESH_TOKEN_TTL", s.App.RefreshTokenTTL)
	s.App.DelegatedTokenMaxTTL = l.Duration("IDENTITY_DELEGATED_TOKEN_MAX_TTL", s.App.DelegatedTokenMaxTTL)
	s.App.ShareLinkDefaultTTL = l.Duration("IDENTITY_SHARE_LINK_DEFAULT_TTL", s.App.ShareLinkDefaultTTL)
	s.App.DecisionTTL = l.Duration("IDENTITY_DECISION_CACHE_TTL", s.App.DecisionTTL)
	s.App.OAuthRedirectURIs = l.List("IDENTITY_OAUTH_REDIRECT_URIS", nil)
	s.App.AllowUnauthenticatedInternalCalls = l.Bool("IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS", false)
	// Signs anyone in without a credential: never outside dev and test, so
	// a production deploy that sets it fails to start instead of serving it.
	s.App.DevSessions = l.Bool("IDENTITY_DEV_SESSIONS", false)
	if s.App.DevSessions && !s.Service.Env.IsDev() {
		l.Fail("IDENTITY_DEV_SESSIONS", "only allowed in dev and test")
	}

	d := credential.DefaultParams
	s.Argon2 = credential.Params{
		MemoryKiB:   uint32(l.IntRange("IDENTITY_ARGON2_MEMORY_KIB", int(d.MemoryKiB), credential.MinMemoryKiB, credential.MaxMemoryKiB)),
		Iterations:  uint32(l.IntRange("IDENTITY_ARGON2_ITERATIONS", int(d.Iterations), 1, credential.MaxIter)),
		Parallelism: uint8(l.IntRange("IDENTITY_ARGON2_PARALLELISM", int(d.Parallelism), 1, 255)),
		SaltLength:  d.SaltLength, KeyLength: d.KeyLength,
	}
	s.Argon2Concurrency = l.IntRange("IDENTITY_ARGON2_MAX_CONCURRENT", 4, 1, 256)

	keySecret := l.OptionalSecret("IDENTITY_SIGNING_KEYS")
	switch {
	case keySecret.IsSet():
		cfg, err := keys.ParseConfig(keySecret.Reveal())
		if err != nil {
			l.Fail("IDENTITY_SIGNING_KEYS", err.Error())
		}
		s.SigningKeys = cfg
	case !s.Service.Env.IsDev():
		l.Fail("IDENTITY_SIGNING_KEYS", "required outside dev and test")
	}
	s.KeyRotation = l.Duration("IDENTITY_KEY_ROTATION_INTERVAL", 0)
	if s.KeyRotation > 0 && (keySecret.IsSet() || !s.Service.Env.IsDev()) {
		l.Fail("IDENTITY_KEY_ROTATION_INTERVAL", "in-process rotation is for single-replica dev with generated keys; rotate IDENTITY_SIGNING_KEYS instead")
	}

	s.Google = oauthClient{ID: l.String("IDENTITY_GOOGLE_CLIENT_ID", ""), Secret: l.OptionalSecret("IDENTITY_GOOGLE_CLIENT_SECRET")}
	s.GitHub = oauthClient{ID: l.String("IDENTITY_GITHUB_CLIENT_ID", ""), Secret: l.OptionalSecret("IDENTITY_GITHUB_CLIENT_SECRET")}
	for _, c := range []struct {
		name string
		oauthClient
	}{{"GOOGLE", s.Google}, {"GITHUB", s.GitHub}} {
		if c.ID != "" && !c.Secret.IsSet() {
			l.Fail("IDENTITY_"+c.name+"_CLIENT_SECRET", "required when the client ID is set")
		}
		if c.ID != "" && len(s.App.OAuthRedirectURIs) == 0 {
			l.Fail("IDENTITY_OAUTH_REDIRECT_URIS", "required when an OAuth provider is configured")
		}
	}

	if fixed := l.String("IDENTITY_FIXED_NOW", ""); fixed != "" {
		t, err := time.Parse(time.RFC3339Nano, fixed)
		switch {
		case err != nil:
			l.Fail("IDENTITY_FIXED_NOW", "must be RFC 3339")
		case !s.Service.Env.IsDev():
			l.Fail("IDENTITY_FIXED_NOW", "only allowed in dev and test (seed ground truth uses a fixed reference time)")
		default:
			s.FixedNow = t
		}
	}
	return s
}

// keyring builds the keyring from configuration, or generates a key in dev
// and test.
func (s settings) keyring(now func() time.Time) (*keys.Keyring, error) {
	opts := keys.Options{RetireAfter: max(s.App.AccessTokenTTL, s.App.DelegatedTokenMaxTTL) + auth.DefaultLeeway, Now: now}
	if len(s.SigningKeys) > 0 {
		return keys.FromConfig(s.SigningKeys, opts)
	}
	key, err := auth.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate signing key: %w", err)
	}
	return keys.New(key, opts)
}

// clock returns the service clock: fixed when IDENTITY_FIXED_NOW is set.
func (s settings) clock() func() time.Time {
	if s.FixedNow.IsZero() {
		return time.Now
	}
	fixed := s.FixedNow
	return func() time.Time { return fixed }
}
