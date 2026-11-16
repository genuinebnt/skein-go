package outbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/platform/events"
)

var errInjected = errors.New("injected failure")

// fakeStore mirrors PostgresStore's claim rules in memory: due rows in id
// order, no locked rows, no row behind a backing-off earlier row of the
// same key, and (exclusive mode) one claim at a time.
type fakeStore struct {
	mu     sync.Mutex
	now    time.Time
	rows   []*fakeRow
	nextID int64
	active bool

	// Fault injection; each is consulted once per call.
	failClaim         func() bool
	failMarkPublished func() bool
	failCommit        func() bool
}

type fakeRow struct {
	rec         Record
	published   bool
	nextAttempt time.Time
	lastError   string
	locked      bool
}

func newFakeStore() *fakeStore {
	never := func() bool { return false }
	return &fakeStore{now: time.Unix(1_800_000_000, 0), failClaim: never, failMarkPublished: never, failCommit: never}
}

func (s *fakeStore) enqueue(topic, key, payload string) events.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	e := events.Envelope{ID: fmt.Sprintf("evt-%03d", s.nextID), Source: "documents", OrderingKey: key, Payload: []byte(payload)}
	s.rows = append(s.rows, &fakeRow{rec: Record{ID: s.nextID, Topic: topic, Envelope: e, CreatedAt: s.now}, nextAttempt: s.now})
	return e
}

func (s *fakeStore) advance(d time.Duration) {
	s.mu.Lock()
	s.now = s.now.Add(d)
	s.mu.Unlock()
}

func (s *fakeStore) unpublished() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.rows {
		if !r.published {
			n++
		}
	}
	return n
}

func (s *fakeStore) row(id int64) fakeRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.rows[id-1]
}

func (s *fakeStore) blockedByEarlier(i int) bool {
	key := s.rows[i].rec.Envelope.OrderingKey
	if key == "" {
		return false
	}
	for _, e := range s.rows[:i] {
		if e.rec.Envelope.OrderingKey == key && !e.published && e.nextAttempt.After(s.now) {
			return true
		}
	}
	return false
}

func (s *fakeStore) Claim(_ context.Context, limit int) (Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failClaim() {
		return nil, errInjected
	}
	b := &fakeBatch{store: s}
	if s.active {
		return b, nil // the advisory lock is held elsewhere
	}
	s.active = true
	for i, r := range s.rows {
		if len(b.records) == limit {
			break
		}
		if r.published || r.locked || r.nextAttempt.After(s.now) || s.blockedByEarlier(i) {
			continue
		}
		r.locked = true
		b.records = append(b.records, r.rec)
	}
	b.holdsLock = true
	return b, nil
}

// CheckInvariants: no row is locked outside an active claim, IDs are
// dense and ascending, and attempts never go negative.
func (s *fakeStore) CheckInvariants() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rows {
		if r.rec.ID != int64(i+1) {
			return fmt.Errorf("row %d has id %d", i, r.rec.ID)
		}
		if r.locked && !s.active {
			return fmt.Errorf("row %d locked with no claim active", r.rec.ID)
		}
		if r.rec.Attempts < 0 {
			return fmt.Errorf("row %d attempts %d", r.rec.ID, r.rec.Attempts)
		}
	}
	return nil
}

type fakeBatch struct {
	store     *fakeStore
	records   []Record
	holdsLock bool
	done      bool
	published []int64
	failed    map[int64]fakeFailure
}

type fakeFailure struct {
	retryAfter time.Duration
	reason     string
}

func (b *fakeBatch) Records() []Record { return b.records }

func (b *fakeBatch) MarkPublished(_ context.Context, ids []int64) error {
	if b.store.failMarkPublished() {
		return errInjected
	}
	b.published = append(b.published, ids...)
	return nil
}

func (b *fakeBatch) MarkFailed(_ context.Context, id int64, retryAfter time.Duration, reason string) error {
	if b.failed == nil {
		b.failed = map[int64]fakeFailure{}
	}
	b.failed[id] = fakeFailure{retryAfter, reason}
	return nil
}

func (b *fakeBatch) end(apply bool) {
	s := b.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.done {
		return
	}
	b.done = true
	for _, rec := range b.records {
		r := s.rows[rec.ID-1]
		r.locked = false
		if !apply {
			continue
		}
		if f, ok := b.failed[rec.ID]; ok {
			r.rec.Attempts++
			r.nextAttempt = s.now.Add(f.retryAfter)
			r.lastError = f.reason
		}
	}
	if apply {
		for _, id := range b.published {
			s.rows[id-1].published = true
		}
	}
	if b.holdsLock {
		s.active = false
	}
}

func (b *fakeBatch) Commit(context.Context) error {
	if b.done {
		return nil
	}
	if b.store.failCommit() {
		b.end(false) // the transaction is gone, as after a lost connection
		return errInjected
	}
	b.end(true)
	return nil
}

func (b *fakeBatch) Rollback(context.Context) error {
	b.end(false)
	return nil
}

// recordingPublisher records every successful publish, in order.
type recordingPublisher struct {
	mu   sync.Mutex
	sent []events.Envelope
	// fail decides, per attempt, whether the broker rejects it.
	fail func(events.Envelope) bool
	// onPublish runs after each successful publish.
	onPublish func()
}

func (p *recordingPublisher) Publish(ctx context.Context, _ string, e events.Envelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	if p.fail != nil && p.fail(e) {
		p.mu.Unlock()
		return errInjected
	}
	p.sent = append(p.sent, e)
	hook := p.onPublish
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (p *recordingPublisher) payloads() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.sent))
	for i, e := range p.sent {
		out[i] = string(e.Payload)
	}
	return out
}
