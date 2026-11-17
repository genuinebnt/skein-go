package app

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

var discard = slog.New(slog.DiscardHandler)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string, ratelimit.Limit, int64) (ratelimit.Decision, error) {
	return ratelimit.Decision{}, errors.New("redis down")
}

func tightLimits(userBurst, ipBurst int64) domain.Limits {
	limits := domain.DefaultLimits()
	for _, b := range domain.Budgets {
		limits[domain.BudgetKey{Budget: b, Subject: domain.SubjectUser}] = ratelimit.PerMinute(60, userBurst)
		limits[domain.BudgetKey{Budget: b, Subject: domain.SubjectIP}] = ratelimit.PerMinute(60, ipBurst)
	}
	return limits
}

// counterValues sums skein.ratelimit.decisions by decision and error.type.
func counterValues(t *testing.T, reader *sdkmetric.ManualReader, name string) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				label := ""
				for _, kv := range dp.Attributes.ToSlice() {
					switch kv.Key {
					case "skein.ratelimit.decision", "error.type", "skein.ratelimit.subject", "skein.idempotency.outcome":
						label += string(kv.Key) + "=" + kv.Value.AsString() + " "
					}
				}
				out[label] += dp.Value
			}
		}
	}
	return out
}

var clientIP = netip.MustParseAddr("203.0.113.5")

func TestThrottlerChecksIPThenUserAndReportsTheTighterBudget(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	th, err := NewThrottler(ratelimit.NewTokenBucket(clock), tightLimits(2, 10), discard, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	v := th.Check(ctx, domain.BudgetAPI, "u1", clientIP)
	if v.Limited() || v.Key.Subject != domain.SubjectUser || v.Decision.Remaining != 1 || v.Decision.Limit != 2 {
		t.Fatalf("first request: %+v", v)
	}
	_ = th.Check(ctx, domain.BudgetAPI, "u1", clientIP)
	v = th.Check(ctx, domain.BudgetAPI, "u1", clientIP)
	if !v.Limited() || v.Key.Subject != domain.SubjectUser || v.Decision.RetryAfter <= 0 {
		t.Fatalf("third request should be limited per user: %+v", v)
	}
	// Another budget is untouched.
	if v := th.Check(ctx, domain.BudgetSearch, "u1", clientIP); v.Limited() {
		t.Fatalf("search budget spent by api requests: %+v", v)
	}
	// Anonymous requests spend only the IP budget.
	if v := th.Check(ctx, domain.BudgetAPI, "", clientIP); v.Limited() || v.Key.Subject != domain.SubjectIP {
		t.Fatalf("anonymous: %+v", v)
	}
}

func TestThrottlerStopsAtAnIPDenialWithoutSpendingTheUserBudget(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	bucket := ratelimit.NewTokenBucket(clock)
	th, _ := NewThrottler(bucket, tightLimits(5, 1), discard, nil)
	ctx := context.Background()
	_ = th.Check(ctx, domain.BudgetAPI, "u1", clientIP)
	v := th.Check(ctx, domain.BudgetAPI, "u1", clientIP)
	if !v.Limited() || v.Key.Subject != domain.SubjectIP {
		t.Fatalf("want an IP denial: %+v", v)
	}
	d, _ := bucket.Allow(ctx, domain.LimiterKey(domain.BudgetAPI, domain.SubjectUser, "u1"), ratelimit.PerMinute(60, 5), 1)
	if d.Remaining != 3 {
		t.Errorf("user budget spent by the denied request: %d left, want 3", d.Remaining)
	}
	if err := bucket.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestThrottlerFailsOpenAndCountsTheFailure(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	th, _ := NewThrottler(failingLimiter{}, domain.DefaultLimits(), discard, sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	v := th.Check(context.Background(), domain.BudgetSync, "u1", clientIP)
	if v.Limited() || v.Checked {
		t.Fatalf("failed limiter must allow without a decision: %+v", v)
	}
	got := counterValues(t, reader, "skein.ratelimit.decisions")
	total := int64(0)
	for label, n := range got {
		if !strings.Contains(label, "error.type=limiter_error") || !strings.Contains(label, "decision=allowed") {
			t.Errorf("unexpected series %q", label)
		}
		total += n
	}
	if total != 2 {
		t.Errorf("counted %d failures, want 2 (ip and user)", total)
	}
}

func TestNewThrottlerRejectsIncompleteLimits(t *testing.T) {
	if _, err := NewThrottler(failingLimiter{}, domain.Limits{}, discard, nil); !errors.Is(err, domain.ErrInvalidLimit) {
		t.Errorf("got %v", err)
	}
}

// memoryStore is a minimal IdempotencyStore for this package's tests; the
// full fake (with expiry) lives in internal/fakes and is checked against
// Redis in redisstore.
type memoryStore struct {
	mu      sync.Mutex
	records map[string]domain.IdempotencyRecord
	fail    bool
}

func (m *memoryStore) Reserve(_ context.Context, key string, p domain.IdempotencyRecord, _ time.Duration) (domain.IdempotencyRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return domain.IdempotencyRecord{}, false, errors.New("down")
	}
	if r, ok := m.records[key]; ok {
		return r, false, nil
	}
	m.records[key] = p
	return domain.IdempotencyRecord{}, true, nil
}

func (m *memoryStore) Complete(_ context.Context, key, owner string, c domain.IdempotencyRecord, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.records[key]; r.Owner != owner || r.State != domain.RecordPending {
		return ErrNotOwner
	}
	m.records[key] = c
	return nil
}

func (m *memoryStore) Release(_ context.Context, key, owner string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.records[key]; r.Owner != owner || r.State != domain.RecordPending {
		return ErrNotOwner
	}
	delete(m.records, key)
	return nil
}

func TestIdempotencyReplaysMismatchesAndReportsInFlight(t *testing.T) {
	store := &memoryStore{records: map[string]domain.IdempotencyRecord{}}
	idem, _ := NewIdempotency(store, IdempotencyOptions{})
	ctx := context.Background()

	first := idem.Begin(ctx, "u1", "k", "fp")
	if first.Outcome != domain.OutcomeProceed {
		t.Fatalf("first: %s", first.Outcome)
	}
	if got := idem.Begin(ctx, "u1", "k", "fp").Outcome; got != domain.OutcomeInFlight {
		t.Errorf("concurrent retry: %s", got)
	}
	if got := idem.Begin(ctx, "u1", "k", "other").Outcome; got != domain.OutcomeMismatch {
		t.Errorf("different body: %s", got)
	}
	if got := idem.Begin(ctx, "u2", "k", "fp").Outcome; got != domain.OutcomeProceed {
		t.Errorf("keys must be per caller: %s", got)
	}
	idem.Finish(ctx, first, domain.StoredResponse{Status: 201, Body: []byte(`{"id":"n1"}`)})
	replay := idem.Begin(ctx, "u1", "k", "fp")
	if replay.Outcome != domain.OutcomeReplay || replay.Replay.Status != 201 || string(replay.Replay.Body) != `{"id":"n1"}` {
		t.Fatalf("replay: %+v", replay)
	}
	// A replay holds nothing, so finishing it changes nothing.
	idem.Finish(ctx, replay, domain.StoredResponse{Status: 500})
	if got := idem.Begin(ctx, "u1", "k", "fp").Outcome; got != domain.OutcomeReplay {
		t.Errorf("finishing a replay changed the record: %s", got)
	}
}

func TestIdempotencyReleasesUnstorableResponsesSoTheRetryRuns(t *testing.T) {
	store := &memoryStore{records: map[string]domain.IdempotencyRecord{}}
	idem, _ := NewIdempotency(store, IdempotencyOptions{})
	ctx := context.Background()
	for _, status := range []int{401, 429, 503} {
		ticket := idem.Begin(ctx, "u1", "k", "fp")
		if ticket.Outcome != domain.OutcomeProceed {
			t.Fatalf("status %d: key still held: %s", status, ticket.Outcome)
		}
		idem.Finish(ctx, ticket, domain.StoredResponse{Status: status})
	}
	ticket := idem.Begin(ctx, "u1", "k", "fp")
	idem.Abandon(ctx, ticket)
	if got := idem.Begin(ctx, "u1", "k", "fp").Outcome; got != domain.OutcomeProceed {
		t.Errorf("abandoned key still held: %s", got)
	}
}

func TestIdempotencyFailsOpenWhenTheStoreFails(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	store := &memoryStore{records: map[string]domain.IdempotencyRecord{}, fail: true}
	idem, _ := NewIdempotency(store, IdempotencyOptions{Meter: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))})
	ticket := idem.Begin(context.Background(), "u1", "k", "fp")
	if ticket.Outcome != domain.OutcomeProceed {
		t.Fatalf("got %s", ticket.Outcome)
	}
	idem.Finish(context.Background(), ticket, domain.StoredResponse{Status: 200}) // must not touch the store
	if got := counterValues(t, reader, "skein.gateway.idempotency.outcomes"); got["skein.idempotency.outcome=error "] != 1 {
		t.Errorf("failure not counted: %v", got)
	}
}

type recordingCloser struct {
	mu      sync.Mutex
	codes   []domain.CloseCode
	onClose func()
}

func (c *recordingCloser) Close(code domain.CloseCode, _ string) {
	c.mu.Lock()
	c.codes = append(c.codes, code)
	c.mu.Unlock()
	if c.onClose != nil {
		c.onClose()
	}
}

func (c *recordingCloser) closedWith() []domain.CloseCode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]domain.CloseCode(nil), c.codes...)
}

func mustAdd(t *testing.T, r *Registry, info SocketInfo, c SocketCloser) func() {
	t.Helper()
	remove, err := r.Add(context.Background(), info, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	return remove
}

// fakeAccess answers CanRead from a set of (user, note) pairs that can no
// longer read, and records the subject and token each check was made with.
type fakeAccess struct {
	mu       sync.Mutex
	lost     map[[2]string]bool
	err      error
	subjects []auth.Subject
	tokens   []string
}

func (a *fakeAccess) CanRead(ctx context.Context, subject auth.Subject, noteID string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	token, _ := auth.TokenFromContext(ctx)
	a.subjects, a.tokens = append(a.subjects, subject), append(a.tokens, token)
	if a.err != nil {
		return false, a.err
	}
	return !a.lost[[2]string{subject.UserID, noteID}], nil
}

func revocations(access NoteAccess, registry *Registry) *Revocations {
	return NewRevocations(&memoryBus{}, registry, access, discard)
}

func TestRevocationClosesOnlySyncSocketsThatCanNoLongerRead(t *testing.T) {
	r := NewRegistry()
	syncN1, syncN2, agent, notes, other := &recordingCloser{}, &recordingCloser{}, &recordingCloser{}, &recordingCloser{}, &recordingCloser{}
	mustAdd(t, r, SocketInfo{Kind: domain.SocketSync, UserID: "u1", NoteID: "n1", Owner: "d1"}, syncN1)
	mustAdd(t, r, SocketInfo{Kind: domain.SocketSync, UserID: "u1", NoteID: "n2", Owner: "d1"}, syncN2)
	mustAdd(t, r, SocketInfo{Kind: domain.SocketAgent, UserID: "u1"}, agent)
	mustAdd(t, r, SocketInfo{Kind: domain.SocketNotifications, UserID: "u1"}, notes)
	mustAdd(t, r, SocketInfo{Kind: domain.SocketSync, UserID: "u2", NoteID: "n1", Owner: "d1"}, other)
	access := &fakeAccess{lost: map[[2]string]bool{{"u1", "n1"}: true}}
	uc := revocations(access, r)

	if o := uc.Enforce(context.Background(), domain.Revocation{UserID: "u1", NoteID: "n1"}); o != (RevocationOutcome{Closed: 1}) {
		t.Fatalf("note revocation: %+v", o)
	}
	if got := syncN1.closedWith(); len(got) != 1 || got[0] != domain.CloseForbidden {
		t.Fatalf("n1 closed with %v", got)
	}
	// Workspace-level: n2 is still readable (a demotion), the agent socket
	// reconnects, n1 is already closed (closing again is a no-op).
	if o := uc.Enforce(context.Background(), domain.Revocation{UserID: "u1", WorkspaceID: "w1"}); o.Kept != 1 || o.Closed != 2 {
		t.Fatalf("workspace revocation: %+v", o)
	}
	if len(syncN2.closedWith()) != 0 {
		t.Error("closed a sync socket whose user can still read the note")
	}
	if got := agent.closedWith(); len(got) != 1 || got[0] != domain.CloseForbidden {
		t.Errorf("agent closed with %v", got)
	}
	if len(notes.closedWith()) != 0 || len(other.closedWith()) != 0 {
		t.Error("closed a notifications socket or another user's socket")
	}
	if err := r.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestDemotedViewerKeepsTheSyncSocketOpen(t *testing.T) {
	r := NewRegistry()
	c := &recordingCloser{}
	mustAdd(t, r, SocketInfo{Kind: domain.SocketSync, UserID: "u1", NoteID: "n1", Owner: "d1"}, c)
	if o := revocations(&fakeAccess{}, r).Enforce(context.Background(), domain.Revocation{UserID: "u1", WorkspaceID: "w1"}); o != (RevocationOutcome{Kept: 1}) {
		t.Fatalf("outcome %+v", o)
	}
	if len(c.closedWith()) != 0 {
		t.Errorf("closed with %v", c.closedWith())
	}
}

func TestAccessIsRecheckedAsTheSocketsOwnUser(t *testing.T) {
	r := NewRegistry()
	token := rand.Text() // generated, never a literal (secret scanning)
	claims := auth.Claims{Subject: auth.Subject{UserID: "u1", AgentRunID: "run-1"}}
	if _, err := r.Add(auth.WithClaims(context.Background(), claims, token),
		SocketInfo{Kind: domain.SocketSync, UserID: "u1", NoteID: "n1", Owner: "d1"}, &recordingCloser{}); err != nil {
		t.Fatal(err)
	}
	access := &fakeAccess{}
	revocations(access, r).Enforce(context.Background(), domain.Revocation{UserID: "u1", NoteID: "n1"})
	if len(access.subjects) != 1 || access.subjects[0] != claims.Subject || access.tokens[0] != token {
		t.Errorf("checked as %v with token %q", access.subjects, access.tokens)
	}
}

func TestAFailedRecheckAsksTheSocketToReconnect(t *testing.T) {
	r := NewRegistry()
	c := &recordingCloser{}
	mustAdd(t, r, SocketInfo{Kind: domain.SocketSync, UserID: "u1", NoteID: "n1", Owner: "d1"}, c)
	access := &fakeAccess{err: errors.New("identity unavailable")}
	if o := revocations(access, r).Enforce(context.Background(), domain.Revocation{UserID: "u1", NoteID: "n1"}); o != (RevocationOutcome{Unverified: 1}) {
		t.Fatalf("outcome %+v", o)
	}
	if got := c.closedWith(); len(got) != 1 || got[0] != domain.CloseServiceRestart {
		t.Errorf("closed with %v, want 1012", got)
	}
}

func TestRerouteClosesSyncSocketsWhoseOwnerChanged(t *testing.T) {
	r := NewRegistry()
	stays, moves := &recordingCloser{}, &recordingCloser{}
	mustAdd(t, r, SocketInfo{Kind: domain.SocketSync, UserID: "u1", NoteID: "n1", Owner: "d1"}, stays)
	mustAdd(t, r, SocketInfo{Kind: domain.SocketSync, UserID: "u1", NoteID: "n2", Owner: "d1"}, moves)
	mustAdd(t, r, SocketInfo{Kind: domain.SocketAgent, UserID: "u1"}, &recordingCloser{})
	n := r.Reroute(func(note string) (string, bool) { return map[string]string{"n1": "d1", "n2": "d2"}[note], true })
	if n != 1 || len(stays.closedWith()) != 0 || moves.closedWith()[0] != domain.CloseServiceRestart {
		t.Fatalf("rerouted %d", n)
	}
}

func TestDrainClosesEverySocketRefusesNewOnesAndWaits(t *testing.T) {
	r := NewRegistry()
	var removes []func()
	closers := make([]*recordingCloser, 3)
	for i := range closers {
		closers[i] = &recordingCloser{}
		removes = append(removes, mustAdd(t, r, SocketInfo{Kind: domain.SocketNotifications, UserID: "u"}, closers[i]))
	}
	done := make(chan error, 1)
	go func() { done <- r.Drain(context.Background()) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(closers[2].closedWith()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := r.Add(context.Background(), SocketInfo{Kind: domain.SocketAgent, UserID: "u"}, &recordingCloser{}); !errors.Is(err, ErrDraining) {
		t.Fatalf("new socket during drain: %v", err)
	}
	for i, remove := range removes {
		select {
		case <-done:
			t.Fatalf("drain returned with %d sockets open", 3-i)
		default:
		}
		remove()
		remove() // idempotent
		if err := r.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, c := range closers {
		if got := c.closedWith(); len(got) != 1 || got[0] != domain.CloseServiceRestart {
			t.Errorf("closed with %v", got)
		}
	}
}

func TestDrainGivesUpAtTheDeadline(t *testing.T) {
	r := NewRegistry()
	mustAdd(t, r, SocketInfo{Kind: domain.SocketAgent, UserID: "u"}, &recordingCloser{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := r.Drain(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v", err)
	}
	if err := NewRegistry().Drain(context.Background()); err != nil {
		t.Errorf("empty drain: %v", err)
	}
}

type memoryBus struct {
	mu        sync.Mutex
	handlers  []func(domain.Revocation)
	fail      bool
	published int
}

func (b *memoryBus) Publish(_ context.Context, r domain.Revocation) error {
	b.mu.Lock()
	if b.fail {
		b.mu.Unlock()
		return errors.New("down")
	}
	b.published++
	hs := append(([]func(domain.Revocation))(nil), b.handlers...)
	b.mu.Unlock()
	for _, h := range hs {
		h(r)
	}
	return nil
}

func (b *memoryBus) Subscribe(ctx context.Context, h func(domain.Revocation)) error {
	b.mu.Lock()
	b.handlers = append(b.handlers, h)
	b.mu.Unlock()
	<-ctx.Done()
	return nil
}

func TestRevocationsCloseLocallyEvenWhenTheBroadcastFails(t *testing.T) {
	bus := &memoryBus{fail: true}
	local := NewRegistry()
	c := &recordingCloser{}
	mustAdd(t, local, SocketInfo{Kind: domain.SocketAgent, UserID: "u1"}, c)
	err := NewRevocations(bus, local, &fakeAccess{}, discard).Apply(context.Background(), domain.Revocation{UserID: "u1"})
	if err == nil {
		t.Error("broadcast failure must be returned so the event is redelivered")
	}
	if len(c.closedWith()) != 1 {
		t.Error("local socket not closed")
	}
}

func TestRevocationsReachOtherReplicasThroughTheBus(t *testing.T) {
	bus := &memoryBus{}
	consumer, other := NewRegistry(), NewRegistry()
	c := &recordingCloser{}
	mustAdd(t, other, SocketInfo{Kind: domain.SocketSync, UserID: "u1", NoteID: "n1", Owner: "d"}, c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	access := &fakeAccess{lost: map[[2]string]bool{{"u1", "n1"}: true}}
	go func() { _ = NewRevocations(bus, other, access, discard).Listen(ctx) }()
	for {
		bus.mu.Lock()
		n := len(bus.handlers)
		bus.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if err := NewRevocations(bus, consumer, access, discard).Apply(ctx, domain.Revocation{UserID: "u1", NoteID: "n1"}); err != nil {
		t.Fatal(err)
	}
	if got := c.closedWith(); len(got) != 1 || got[0] != domain.CloseForbidden {
		t.Errorf("other replica's socket closed with %v", got)
	}
}
