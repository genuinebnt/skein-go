package fakes

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/services/gateway/internal/app"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// ErrInjected is returned by fakes told to fail.
var ErrInjected = errors.New("fakes: injected failure")

type storedRecord struct {
	record  domain.IdempotencyRecord
	expires time.Time
}

// IdempotencyStore is an in-memory app.IdempotencyStore with a settable
// clock and failure switch.
type IdempotencyStore struct {
	mu      sync.Mutex
	now     func() time.Time
	records map[string]storedRecord
	failing bool
}

// NewIdempotencyStore returns an empty store; now defaults to time.Now.
func NewIdempotencyStore(now func() time.Time) *IdempotencyStore {
	if now == nil {
		now = time.Now
	}
	return &IdempotencyStore{now: now, records: map[string]storedRecord{}}
}

// SetFailing makes every call fail with ErrInjected, as Redis being down
// would.
func (s *IdempotencyStore) SetFailing(failing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = failing
}

func (s *IdempotencyStore) liveLocked(key string) (domain.IdempotencyRecord, bool) {
	r, ok := s.records[key]
	if !ok {
		return domain.IdempotencyRecord{}, false
	}
	if !s.now().Before(r.expires) {
		delete(s.records, key)
		return domain.IdempotencyRecord{}, false
	}
	return r.record, true
}

// Reserve implements app.IdempotencyStore.
func (s *IdempotencyStore) Reserve(_ context.Context, key string, pending domain.IdempotencyRecord, ttl time.Duration) (domain.IdempotencyRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failing {
		return domain.IdempotencyRecord{}, false, ErrInjected
	}
	if existing, ok := s.liveLocked(key); ok {
		return existing, false, nil
	}
	s.records[key] = storedRecord{record: pending, expires: s.now().Add(ttl)}
	return domain.IdempotencyRecord{}, true, nil
}

func (s *IdempotencyStore) heldLocked(key, owner string) bool {
	r, ok := s.liveLocked(key)
	return ok && r.State == domain.RecordPending && r.Owner == owner
}

// Complete implements app.IdempotencyStore.
func (s *IdempotencyStore) Complete(_ context.Context, key, owner string, complete domain.IdempotencyRecord, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failing {
		return ErrInjected
	}
	if !s.heldLocked(key, owner) {
		return app.ErrNotOwner
	}
	s.records[key] = storedRecord{record: complete, expires: s.now().Add(ttl)}
	return nil
}

// Release implements app.IdempotencyStore.
func (s *IdempotencyStore) Release(_ context.Context, key, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failing {
		return ErrInjected
	}
	if !s.heldLocked(key, owner) {
		return app.ErrNotOwner
	}
	delete(s.records, key)
	return nil
}

// Len returns the number of live records.
func (s *IdempotencyStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k := range s.records {
		if _, ok := s.liveLocked(k); ok {
			n++
		}
	}
	return n
}

// CheckInvariants verifies that pending records have an owner and
// complete ones a response status.
func (s *IdempotencyStore) CheckInvariants() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, r := range s.records {
		switch r.record.State {
		case domain.RecordPending:
			if r.record.Owner == "" {
				return fmt.Errorf("fakes: pending record %q has no owner", k)
			}
		case domain.RecordComplete:
			if r.record.Response.Status == 0 {
				return fmt.Errorf("fakes: complete record %q has no status", k)
			}
		default:
			return fmt.Errorf("fakes: record %q has state %q", k, r.record.State)
		}
		if r.record.Fingerprint == "" {
			return fmt.Errorf("fakes: record %q has no fingerprint", k)
		}
	}
	return nil
}

// RevocationBus is an in-memory app.RevocationBus: every subscriber sees
// every publication, as every replica does with Redis pub/sub.
type RevocationBus struct {
	mu          sync.Mutex
	subscribers map[int]func(domain.Revocation)
	next        int
	failing     bool
	published   []domain.Revocation
}

// NewRevocationBus returns a bus with no subscribers.
func NewRevocationBus() *RevocationBus {
	return &RevocationBus{subscribers: map[int]func(domain.Revocation){}}
}

// SetFailing makes Publish fail with ErrInjected.
func (b *RevocationBus) SetFailing(failing bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failing = failing
}

// Publish implements app.RevocationBus.
func (b *RevocationBus) Publish(_ context.Context, r domain.Revocation) error {
	b.mu.Lock()
	if b.failing {
		b.mu.Unlock()
		return ErrInjected
	}
	b.published = append(b.published, r)
	handlers := make([]func(domain.Revocation), 0, len(b.subscribers))
	for _, h := range b.subscribers {
		handlers = append(handlers, h)
	}
	b.mu.Unlock()
	for _, h := range handlers {
		h(r)
	}
	return nil
}

// Subscribe implements app.RevocationBus.
func (b *RevocationBus) Subscribe(ctx context.Context, handle func(domain.Revocation)) error {
	b.mu.Lock()
	b.next++
	id := b.next
	b.subscribers[id] = handle
	b.mu.Unlock()
	<-ctx.Done()
	b.mu.Lock()
	delete(b.subscribers, id)
	b.mu.Unlock()
	return nil
}

// Subscribers returns the number of active subscriptions.
func (b *RevocationBus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

// Published returns every successful publication.
func (b *RevocationBus) Published() []domain.Revocation {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]domain.Revocation(nil), b.published...)
}
