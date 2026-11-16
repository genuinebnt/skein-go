package outbox

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/internal/testseed"
)

func noJitter(d time.Duration) time.Duration { return d }

func newRelay(t *testing.T, store Store, pub events.Publisher, cfg RelayConfig) *Relay {
	t.Helper()
	if cfg.Jitter == nil {
		cfg.Jitter = noJitter
	}
	r, err := NewRelay(store, pub, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func runOnce(t *testing.T, r *Relay, store *fakeStore) Result {
	t.Helper()
	res, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if err := store.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRelayPublishesAndMarksRows(t *testing.T) {
	store := newFakeStore()
	for _, p := range []string{"a1", "a2", "b1"} {
		store.enqueue("documents.events", p[:1], p)
	}
	pub := &recordingPublisher{}
	r := newRelay(t, store, pub, RelayConfig{Concurrency: 1})
	res := runOnce(t, r, store)
	if res.Published != 3 || store.unpublished() != 0 {
		t.Fatalf("result %+v, unpublished %d", res, store.unpublished())
	}
	if got := pub.payloads(); !slices.Equal(got, []string{"a1", "a2", "b1"}) {
		t.Fatalf("published %v", got)
	}
	if res := runOnce(t, r, store); res.Claimed != 0 {
		t.Fatalf("published rows claimed again: %+v", res)
	}
}

func TestFailedEventHoldsBackItsKeyButNotOthers(t *testing.T) {
	store := newFakeStore()
	for _, p := range []string{"a1", "b1", "a2", "b2", "x"} {
		key := p[:1]
		if p == "x" {
			key = ""
		}
		store.enqueue("t", key, p)
	}
	failA1 := true
	pub := &recordingPublisher{fail: func(e events.Envelope) bool { return string(e.Payload) == "a1" && failA1 }}
	r := newRelay(t, store, pub, RelayConfig{InitialBackoff: time.Second})

	res := runOnce(t, r, store)
	if res.Published != 3 || res.Failed != 1 || res.Skipped != 1 {
		t.Fatalf("first batch %+v, want b1 b2 x published, a1 failed, a2 skipped", res)
	}
	if row := store.row(1); row.rec.Attempts != 1 || row.lastError == "" {
		t.Fatalf("a1 not marked failed: %+v", row)
	}
	// a1 is backing off, so a2 must not be claimed on its own.
	if res := runOnce(t, r, store); res.Claimed != 0 {
		t.Fatalf("claimed %+v while a1 backs off", res)
	}
	failA1 = false
	store.advance(time.Second)
	runOnce(t, r, store)
	got := pub.payloads()
	if i, j := slices.Index(got, "a1"), slices.Index(got, "a2"); i < 0 || j < i {
		t.Fatalf("published %v: a2 overtook a1", got)
	}
}

func TestBackoffDoublesUpToTheMaximum(t *testing.T) {
	r := newRelay(t, newFakeStore(), &recordingPublisher{}, RelayConfig{InitialBackoff: time.Second, MaxBackoff: 10 * time.Second})
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, w := range want {
		if got := r.Backoff(int32(i + 1)); got != w {
			t.Errorf("Backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := r.Backoff(1000); got != 10*time.Second {
		t.Errorf("Backoff(1000) = %v", got)
	}
}

func TestDefaultJitterStaysBetweenHalfAndAll(t *testing.T) {
	r := newRelay(t, newFakeStore(), &recordingPublisher{}, RelayConfig{Jitter: nil})
	r.cfg.Jitter = nil
	r.cfg.defaults()
	for range 1000 {
		if d := r.cfg.Jitter(time.Second); d < 500*time.Millisecond || d > time.Second {
			t.Fatalf("jitter %v outside [0.5s, 1s]", d)
		}
	}
}

func TestCrashBetweenPublishAndCommitDuplicatesButNeverLoses(t *testing.T) {
	store := newFakeStore()
	store.enqueue("t", "n1", "e1")
	store.enqueue("t", "n1", "e2")
	crash := true
	store.failCommit = func() bool { c := crash; crash = false; return c }
	pub := &recordingPublisher{}

	// The first relay publishes both, then "crashes" before its commit lands.
	if _, err := newRelay(t, store, pub, RelayConfig{}).RunOnce(context.Background()); err == nil {
		t.Fatal("injected commit failure not reported")
	}
	if store.unpublished() != 2 {
		t.Fatal("rows marked published although the commit failed")
	}
	// A restarted relay publishes them again: duplicates, in order.
	runOnce(t, newRelay(t, store, pub, RelayConfig{}), store)
	if got := pub.payloads(); !slices.Equal(got, []string{"e1", "e2", "e1", "e2"}) {
		t.Fatalf("published %v, want both twice in order", got)
	}
	if store.unpublished() != 0 {
		t.Fatal("rows not published after restart")
	}
}

func TestShutdownMidBatchStillMarksWhatWasPublished(t *testing.T) {
	store := newFakeStore()
	for _, p := range []string{"e1", "e2", "e3", "e4"} {
		store.enqueue("t", "n1", p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	published := 0
	pub := &recordingPublisher{onPublish: func() {
		if published++; published == 2 {
			cancel()
		}
	}}
	r := newRelay(t, store, pub, RelayConfig{})
	res, err := r.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Published != 2 || res.Failed != 0 || store.unpublished() != 2 {
		t.Fatalf("result %+v, unpublished %d: want e1 e2 marked, e3 e4 untouched", res, store.unpublished())
	}
	if row := store.row(3); row.rec.Attempts != 0 {
		t.Fatalf("an interrupted row was counted as a failure: %+v", row)
	}
}

func TestOnlyOneRelayClaimsAtATime(t *testing.T) {
	store := newFakeStore()
	store.enqueue("t", "n1", "e1")
	first, err := store.Claim(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if second, _ := store.Claim(context.Background(), 10); len(second.Records()) != 0 {
		t.Fatal("a second relay claimed while the first held the lock")
	}
	_ = first.Rollback(context.Background())
}

// TestRelayUnderRandomFaultsNeverLosesOrReorders is the main property
// test: random publish failures, lost commits and failed marks, with a new
// relay after each crash, until everything is published. Every event must
// be published at least once, each key's first publications must follow
// enqueue order, and duplicates can only come from crashes.
func TestRelayUnderRandomFaultsNeverLosesOrReorders(t *testing.T) {
	seed := testseed.Seed(t)
	r := testseed.Rand(seed)
	var mu sync.Mutex // the publisher runs concurrently; rand is not safe
	chance := func(p float64) func() bool {
		return func() bool { mu.Lock(); defer mu.Unlock(); return r.Float64() < p }
	}
	for trial := range 20 {
		store := newFakeStore()
		keys := []string{"", "n1", "n2", "n3", "n4"}
		order := map[string][]string{}
		for i := range 200 {
			mu.Lock()
			key := keys[r.IntN(len(keys))]
			mu.Unlock()
			e := store.enqueue("documents.events", key, "")
			order[key] = append(order[key], e.ID)
			_ = i
		}
		store.failCommit = chance(0.1)
		store.failMarkPublished = chance(0.05)
		store.failClaim = chance(0.05)
		failPublish := chance(0.2)
		pub := &recordingPublisher{fail: func(events.Envelope) bool { return failPublish() }}
		cfg := RelayConfig{BatchSize: 1 + trial%30, InitialBackoff: time.Second, MaxBackoff: 8 * time.Second}
		relay := newRelay(t, store, pub, cfg)

		crashes := 0
		for rounds := 0; store.unpublished() > 0; rounds++ {
			if rounds > 10_000 {
				t.Fatalf("trial %d: never drained", trial)
			}
			if _, err := relay.RunOnce(context.Background()); err != nil {
				crashes++
				relay = newRelay(t, store, pub, cfg) // restart after a crash
			}
			if err := store.CheckInvariants(); err != nil {
				t.Fatalf("trial %d: %v", trial, err)
			}
			mu.Lock()
			step := time.Duration(r.IntN(3000)) * time.Millisecond
			mu.Unlock()
			store.advance(step)
		}

		firstSeen := map[string]bool{}
		firsts := map[string][]string{}
		for _, e := range pub.sent {
			if !firstSeen[e.ID] {
				firstSeen[e.ID] = true
				firsts[e.OrderingKey] = append(firsts[e.OrderingKey], e.ID)
			}
		}
		for key, want := range order {
			got := firsts[key]
			if key == "" {
				slices.Sort(got)
				want = slices.Clone(want)
				slices.Sort(want)
			}
			if !slices.Equal(got, want) {
				t.Fatalf("trial %d key %q: first publications %v, want %v", trial, key, got, want)
			}
		}
		if dups := len(pub.sent) - len(firstSeen); dups > crashes*cfg.BatchSize {
			t.Fatalf("trial %d: %d duplicates from %d crashes of batch size %d", trial, dups, crashes, cfg.BatchSize)
		}
	}
}

func TestRunWakesOnNotifyAndStopsOnCancel(t *testing.T) {
	store := newFakeStore()
	pub := &recordingPublisher{}
	r := newRelay(t, store, pub, RelayConfig{PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	time.Sleep(10 * time.Millisecond)
	store.enqueue("t", "", "e1")
	r.Notify()
	deadline := time.After(2 * time.Second)
	for store.unpublished() > 0 {
		select {
		case <-deadline:
			t.Fatal("Notify did not wake the relay")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}
