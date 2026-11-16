package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// IdempotencyOptions configure NewIdempotency. Zero values take defaults.
type IdempotencyOptions struct {
	// TTL is how long a completed response is replayed: 24 hours, as
	// documented.
	TTL time.Duration
	// PendingTTL bounds a reservation whose request never finished (the
	// gateway crashed), after which the key can be used again. It must
	// exceed the longest request; default 2 minutes.
	PendingTTL time.Duration
	Logger     *slog.Logger
	Meter      metric.MeterProvider
}

// Idempotency is the idempotency-key use case.
type Idempotency struct {
	store    IdempotencyStore
	opts     IdempotencyOptions
	outcomes metric.Int64Counter
}

// Ticket is a request's place in the idempotency protocol, from Begin to
// Finish or Abandon.
type Ticket struct {
	Outcome domain.Outcome
	// Replay is the stored response when Outcome is OutcomeReplay.
	Replay domain.StoredResponse

	key, owner, fingerprint string
	// held is true when this request holds a pending record that it must
	// complete or release.
	held bool
}

// NewIdempotency returns the use case over store.
func NewIdempotency(store IdempotencyStore, opts IdempotencyOptions) (*Idempotency, error) {
	if opts.TTL == 0 {
		opts.TTL = 24 * time.Hour
	}
	if opts.PendingTTL == 0 {
		opts.PendingTTL = 2 * time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Meter == nil {
		opts.Meter = noop.NewMeterProvider()
	}
	outcomes, err := opts.Meter.Meter(meterName).Int64Counter("skein.gateway.idempotency.outcomes",
		metric.WithUnit("{request}"), metric.WithDescription("Requests with an Idempotency-Key, by outcome"))
	if err != nil {
		return nil, err
	}
	return &Idempotency{store: store, opts: opts, outcomes: outcomes}, nil
}

// ScopedKey is the store key for a caller's idempotency key; keys are per
// caller, so two users can never see each other's responses.
func ScopedKey(userID, key string) string { return userID + ":" + key }

// Begin reserves key for this request or reports what to do instead.
func (s *Idempotency) Begin(ctx context.Context, userID, key, fingerprint string) Ticket {
	t := Ticket{key: ScopedKey(userID, key), owner: newOwner(), fingerprint: fingerprint}
	pending := domain.IdempotencyRecord{State: domain.RecordPending, Fingerprint: fingerprint, Owner: t.owner}
	existing, reserved, err := s.store.Reserve(ctx, t.key, pending, s.opts.PendingTTL)
	switch {
	case err != nil:
		s.count(ctx, "error")
		s.opts.Logger.WarnContext(ctx, "idempotency store failed; running request without it", slog.String("error", err.Error()))
		t.Outcome = domain.OutcomeProceed
	case reserved:
		s.count(ctx, "reserved")
		t.Outcome, t.held = domain.OutcomeProceed, true
	default:
		t.Outcome = domain.Decide(existing, fingerprint)
		t.Replay = existing.Response
		s.count(ctx, string(t.Outcome))
	}
	return t
}

// Finish stores resp for replay when it is storable, and otherwise
// releases the key so a retry runs again. It does nothing for a ticket
// that holds no reservation.
func (s *Idempotency) Finish(ctx context.Context, t Ticket, resp domain.StoredResponse) {
	if !t.held {
		return
	}
	if !domain.Storable(resp.Status) {
		s.Abandon(ctx, t)
		return
	}
	complete := domain.IdempotencyRecord{State: domain.RecordComplete, Fingerprint: t.fingerprint, Response: resp}
	if err := s.store.Complete(ctx, t.key, t.owner, complete, s.opts.TTL); err != nil {
		s.logFailure(ctx, "complete", err)
		return
	}
	s.count(ctx, "stored")
}

// Abandon releases the key without storing anything, for responses that
// cannot be kept (too large, or the handler panicked).
func (s *Idempotency) Abandon(ctx context.Context, t Ticket) {
	if !t.held {
		return
	}
	if err := s.store.Release(ctx, t.key, t.owner); err != nil {
		s.logFailure(ctx, "release", err)
		return
	}
	s.count(ctx, "released")
}

func (s *Idempotency) logFailure(ctx context.Context, operation string, err error) {
	if errors.Is(err, ErrNotOwner) {
		// The request outlived PendingTTL and another request took the
		// key. That request's outcome stands.
		s.opts.Logger.WarnContext(ctx, "idempotency reservation expired before the request finished", slog.String("operation", operation))
		s.count(ctx, "expired")
		return
	}
	s.opts.Logger.WarnContext(ctx, "idempotency store failed", slog.String("operation", operation), slog.String("error", err.Error()))
	s.count(ctx, "error")
}

func (s *Idempotency) count(ctx context.Context, outcome string) {
	s.outcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("skein.idempotency.outcome", outcome)))
}

// newOwner returns a random reservation token. crypto/rand never fails on
// supported platforms (it panics instead), so the error is not checked.
func newOwner() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
