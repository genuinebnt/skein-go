// Package app holds identity's use cases and the ports they need (ports.go).
// Each exported method of Service is one use case; the gRPC adapter calls
// them with the caller taken from the verified access token, and tests call
// them with in-memory fakes (internal/fakes).
//
// # Transactions and events
//
// Every state change runs in one Store transaction together with the
// AccessChanged events it causes (transactional outbox, ADR-0006), so an
// access change is never visible without its event, or the reverse. After
// the transaction commits, the use case drops the Authorize decisions the
// change affects from the decision cache (tag invalidation). A crash
// between commit and invalidation leaves stale entries for at most the
// cache TTL; that bound is why the TTL is short.
//
// # What is cached, and why
//
// Authorize decisions, per (subject, action, resource), for DecisionTTL
// (default 30 s). Every service calls Authorize before acting, documents
// once per batch of operations, so the same few decisions are asked for
// many times a second while a note is being edited. Each entry is tagged
// with its user, note and workspace; AccessChanged invalidates by tag, and
// documents' note events (created, trashed, restored, purged) invalidate
// the note's tag. Decisions about unknown notes or workspaces are never
// cached, because the projection may simply be behind documents.
//
// # Rate limits
//
// SignIn is limited per account (the normalised email) and per client IP,
// in front of the expensive Argon2id verification. Both limits fail open
// when Redis is down, as platform/ratelimit does everywhere; the per-IP
// limit at the gateway still applies.
//
// # Porting notes
//
// Service becomes a struct of `Arc<dyn Port>` fields; each use case an
// `async fn`. InTx becomes a closure taking `&mut Tx` (sqlx Transaction).
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// Config holds identity's policy knobs. DefaultConfig gives the documented
// values.
type Config struct {
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	// DelegatedTokenMaxTTL caps agent-run tokens (ADR-0009).
	DelegatedTokenMaxTTL time.Duration
	// ShareLinkDefaultTTL is the expiry of links created without one; 0
	// means such links never expire.
	ShareLinkDefaultTTL time.Duration
	DecisionTTL         time.Duration
	// MaxChecksPerAuthorize bounds one Authorize batch.
	MaxChecksPerAuthorize int
	SignInPerAccount      ratelimit.Limit
	SignInPerIP           ratelimit.Limit
	// OAuthRedirectURIs are the only redirect URIs a flow may use (exact
	// match), so a code cannot be redeemed for an attacker's callback.
	OAuthRedirectURIs []string
	// AllowUnauthenticatedInternalCalls lets callers without a token use
	// Authorize and ListReadableNotes (event consumers have no user
	// token). Off by default: auth fails closed.
	AllowUnauthenticatedInternalCalls bool
}

// DefaultConfig returns the defaults: 15-minute access tokens (as in
// docs/api/identity.md), 30-day refresh tokens, 15-minute delegated tokens,
// 7-day share links, 30-second decision cache, 5 sign-ins a minute per
// account (burst 10) and 30 per IP (burst 60).
func DefaultConfig() Config {
	return Config{
		AccessTokenTTL:        15 * time.Minute,
		RefreshTokenTTL:       30 * 24 * time.Hour,
		DelegatedTokenMaxTTL:  15 * time.Minute,
		ShareLinkDefaultTTL:   7 * 24 * time.Hour,
		DecisionTTL:           30 * time.Second,
		MaxChecksPerAuthorize: 1000,
		SignInPerAccount:      ratelimit.PerMinute(5, 10),
		SignInPerIP:           ratelimit.PerMinute(30, 60),
	}
}

// Deps are the ports a Service needs. Cache, Limiter, Providers and
// MeterProvider may be nil.
type Deps struct {
	Store         Store
	Hasher        *credential.Hasher
	Issuer        TokenIssuer
	Cache         DecisionCache
	Limiter       Limiter
	Providers     map[OAuthProviderKind]OAuthProvider
	Clock         Clock
	Logger        *slog.Logger
	Random        io.Reader // token entropy; crypto/rand when nil
	MeterProvider metric.MeterProvider
}

// Service implements identity's use cases.
type Service struct {
	cfg     Config
	store   Store
	hasher  *credential.Hasher
	issuer  TokenIssuer
	cache   DecisionCache
	limiter Limiter
	oauth   map[OAuthProviderKind]OAuthProvider
	now     Clock
	log     *slog.Logger
	random  io.Reader
	metrics metrics
}

// New returns a Service.
func New(cfg Config, d Deps) (*Service, error) {
	if d.Store == nil || d.Hasher == nil || d.Issuer == nil {
		return nil, errors.New("app: Store, Hasher and Issuer are required")
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Logger == nil {
		d.Logger = logging.Discard()
	}
	m, err := newMetrics(d.MeterProvider)
	if err != nil {
		return nil, err
	}
	return &Service{
		cfg: cfg, store: d.Store, hasher: d.Hasher, issuer: d.Issuer, cache: d.Cache,
		limiter: d.Limiter, oauth: d.Providers, now: d.Clock, log: d.Logger, random: d.Random, metrics: m,
	}, nil
}

// Now returns the service clock's current time.
func (s *Service) Now() time.Time { return s.now() }

// RateLimitError is returned when a sign-in is over its limit.
type RateLimitError struct {
	RetryAfter time.Duration
}

// Error implements error.
func (e *RateLimitError) Error() string {
	return fmt.Sprintf("too many sign-in attempts; retry after %s", e.RetryAfter)
}

// metrics are identity's own instruments, named per docs/observability.md.
type metrics struct {
	rateLimit     metric.Int64Counter
	cacheLookups  metric.Int64Counter
	invalidations metric.Int64Counter
}

func newMetrics(mp metric.MeterProvider) (metrics, error) {
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	meter := mp.Meter("github.com/genuinebnt/skein-go/services/identity")
	var m metrics
	var err error
	if m.rateLimit, err = meter.Int64Counter("skein.ratelimit.decisions", metric.WithUnit("{request}"),
		metric.WithDescription("Rate limiter decisions.")); err != nil {
		return m, err
	}
	if m.cacheLookups, err = meter.Int64Counter("skein.cache.lookups", metric.WithUnit("{lookup}"),
		metric.WithDescription("Cache lookups by result.")); err != nil {
		return m, err
	}
	if m.invalidations, err = meter.Int64Counter("skein.cache.invalidations", metric.WithUnit("{entry}"),
		metric.WithDescription("Cache invalidation requests by reason.")); err != nil {
		return m, err
	}
	return m, nil
}

func (m metrics) lookup(ctx context.Context, hit bool) {
	result := "miss"
	if hit {
		result = "hit"
	}
	m.cacheLookups.Add(ctx, 1, metric.WithAttributes(
		attribute.String("skein.cache.name", "authorize"), attribute.String("skein.cache.result", result)))
}

// invalidate drops cached decisions with any of tags. Failures are logged,
// not returned: the write has committed, and the TTL bounds staleness.
func (s *Service) invalidate(ctx context.Context, reason string, tags ...string) {
	if s.cache == nil || len(tags) == 0 {
		return
	}
	s.metrics.invalidations.Add(ctx, int64(len(tags)), metric.WithAttributes(
		attribute.String("skein.cache.name", "authorize"), attribute.String("skein.cache.invalidation.reason", reason)))
	if err := s.cache.InvalidateTags(ctx, tags...); err != nil {
		s.log.WarnContext(ctx, "decision cache invalidation failed", "error", err)
	}
}

func (s *Service) invalidateChanges(ctx context.Context, changes []domain.AccessChange) {
	var tags []string
	for _, c := range changes {
		tags = append(tags, c.CacheTags()...)
	}
	slices.Sort(tags)
	s.invalidate(ctx, "write", slices.Compact(tags)...)
}

// workspaceRole returns the user's active role in ws, or none.
func workspaceRole(ctx context.Context, tx Tx, ws domain.WorkspaceID, user domain.UserID, now time.Time) (domain.WorkspaceRole, error) {
	m, err := tx.Membership(ctx, ws, user)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return domain.WorkspaceRoleNone, nil
	case err != nil:
		return domain.WorkspaceRoleNone, err
	case !m.ActiveAt(now):
		return domain.WorkspaceRoleNone, nil
	}
	return m.Role, nil
}

// requireUser returns an error unless the caller is a signed-in user
// (possibly through an agent run).
func requireUser(caller domain.Subject) error {
	if (caller.Kind != domain.SubjectUser && caller.Kind != domain.SubjectAgentRun) || caller.UserID == "" {
		return domain.ErrUnauthenticated
	}
	return nil
}
