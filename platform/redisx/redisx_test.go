package redisx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/genuinebnt/skein-go/platform/internal/testseed"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

// storeUnderTest pairs a store with a way to move its time forward.
type storeUnderTest struct {
	name    string
	store   Store
	advance func(time.Duration)
	check   func() error
}

func memoryStore() storeUnderTest {
	c := &clock{now: time.Unix(1_800_000_000, 0)}
	s := NewMemoryStore(c.Now)
	return storeUnderTest{name: "memory", store: s, advance: c.advance, check: s.CheckInvariants}
}

func redisStore(t *testing.T) (storeUnderTest, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return storeUnderTest{name: "redis", store: NewRedisStore(client, "search"), advance: mr.FastForward, check: func() error { return nil }}, mr
}

func stores(t *testing.T) []storeUnderTest {
	r, _ := redisStore(t)
	return []storeUnderTest{memoryStore(), r}
}

func TestStoresExpireEntries(t *testing.T) {
	ctx := context.Background()
	for _, s := range stores(t) {
		if err := s.store.Set(ctx, "k", []byte("v"), time.Second); err != nil {
			t.Fatal(err)
		}
		s.advance(999 * time.Millisecond)
		if _, found, _ := s.store.Get(ctx, "k"); !found {
			t.Errorf("%s: expired early", s.name)
		}
		s.advance(2 * time.Millisecond)
		if _, found, _ := s.store.Get(ctx, "k"); found {
			t.Errorf("%s: did not expire", s.name)
		}
		if err := s.check(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoresRejectEntriesWithoutExpiry(t *testing.T) {
	for _, s := range stores(t) {
		if err := s.store.Set(context.Background(), "k", nil, 0); !errors.Is(err, ErrInvalidEntry) {
			t.Errorf("%s: %v", s.name, err)
		}
	}
}

func TestInvalidateTagDropsTaggedEntriesAcrossCaches(t *testing.T) {
	ctx := context.Background()
	for _, s := range stores(t) {
		titles, _ := New(s.store, Options[string]{Name: "titles", TTL: time.Minute})
		snippets, _ := New(s.store, Options[string]{Name: "snippets", TTL: time.Minute})
		_ = titles.Set(ctx, "n1", "Groceries", "note:n1")
		_ = snippets.Set(ctx, "n1:q", "...eggs...", "note:n1", "query:q")
		_ = titles.Set(ctx, "n2", "Plans", "note:n2")

		inv, _ := NewInvalidator(s.store, nil)
		n, err := inv.InvalidateTags(ctx, "note:n1")
		if err != nil || n != 2 {
			t.Fatalf("%s: removed %d (%v), want 2", s.name, n, err)
		}
		if _, found, _ := titles.Get(ctx, "n1"); found {
			t.Errorf("%s: titles n1 survived", s.name)
		}
		if _, found, _ := snippets.Get(ctx, "n1:q"); found {
			t.Errorf("%s: snippets n1:q survived", s.name)
		}
		if v, found, _ := titles.Get(ctx, "n2"); !found || v != "Plans" {
			t.Errorf("%s: untagged entry dropped", s.name)
		}
		if n, _ := inv.InvalidateTags(ctx, "query:q"); n != 0 {
			t.Errorf("%s: second invalidation removed %d already-gone entries", s.name, n)
		}
		if err := s.check(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRedisKeysAreNamespaced(t *testing.T) {
	s, mr := redisStore(t)
	c, _ := New(s.store, Options[int]{Name: "counts", TTL: time.Minute})
	_ = c.Set(context.Background(), "a", 1, "note:x")
	for _, k := range []string{"search:c:counts:a", "search:t:note:x"} {
		if !mr.Exists(k) {
			t.Errorf("missing key %s; have %v", k, mr.Keys())
		}
	}
	if ttl := mr.TTL("search:t:note:x"); ttl <= 0 || ttl > time.Minute {
		t.Errorf("tag set TTL %v, want up to the entry's minute", ttl)
	}
}

// TestRedisStoreMatchesMemoryStore runs the same random operations on both
// stores and requires identical results: the memory store is the
// reference the Lua scripts are checked against.
func TestRedisStoreMatchesMemoryStore(t *testing.T) {
	r := testseed.Rand(testseed.Seed(t))
	ctx := context.Background()
	mem := memoryStore()
	red, _ := redisStore(t)
	keys := []string{"a", "b", "c", "d"}
	tags := []string{"t1", "t2", "t3"}
	pick := func(from []string) string { return from[r.IntN(len(from))] }
	for step := range 2000 {
		var desc string
		var a, b any
		switch r.IntN(5) {
		case 0:
			k, ttl := pick(keys), time.Duration(1+r.IntN(5))*time.Second
			var ts []string
			for _, tag := range tags {
				if r.IntN(2) == 0 {
					ts = append(ts, tag)
				}
			}
			v := []byte(fmt.Sprint(step))
			desc = fmt.Sprintf("Set(%s, %v, %v)", k, ttl, ts)
			a, b = mem.store.Set(ctx, k, v, ttl, ts...), red.store.Set(ctx, k, v, ttl, ts...)
		case 1:
			k := pick(keys)
			v1, f1, e1 := mem.store.Get(ctx, k)
			v2, f2, e2 := red.store.Get(ctx, k)
			desc, a, b = "Get("+k+")", fmt.Sprint(string(v1), f1, e1), fmt.Sprint(string(v2), f2, e2)
		case 2:
			k := pick(keys)
			n1, e1 := mem.store.Delete(ctx, k)
			n2, e2 := red.store.Delete(ctx, k)
			desc, a, b = "Delete("+k+")", fmt.Sprint(n1, e1), fmt.Sprint(n2, e2)
		case 3:
			tag := pick(tags)
			n1, e1 := mem.store.InvalidateTags(ctx, tag)
			n2, e2 := red.store.InvalidateTags(ctx, tag)
			desc, a, b = "InvalidateTags("+tag+")", fmt.Sprint(n1, e1), fmt.Sprint(n2, e2)
		default:
			d := time.Duration(r.IntN(2000)) * time.Millisecond
			desc = fmt.Sprintf("advance %v", d)
			mem.advance(d)
			red.advance(d)
		}
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Fatalf("step %d %s: memory %v, redis %v", step, desc, a, b)
		}
		if err := mem.check(); err != nil {
			t.Fatalf("step %d %s: %v", step, desc, err)
		}
	}
}

func TestGetOrLoadLoadsOnceUnderStampede(t *testing.T) {
	store := memoryStore()
	c, _ := New(store.store, Options[string]{Name: "notes", TTL: time.Minute})
	var loads atomic.Int32
	release := make(chan struct{})
	loader := func(context.Context) (string, []string, error) {
		loads.Add(1)
		<-release
		return "value", []string{"note:1"}, nil
	}
	const callers = 50
	var wg sync.WaitGroup
	results := make(chan string, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.GetOrLoad(context.Background(), "1", loader)
			if err != nil {
				t.Error(err)
			}
			results <- v
		}()
	}
	time.Sleep(50 * time.Millisecond) // let every caller reach the flight
	close(release)
	wg.Wait()
	close(results)
	for v := range results {
		if v != "value" {
			t.Fatalf("got %q", v)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("loader ran %d times, want 1", loads.Load())
	}
	if v, found, _ := c.Get(context.Background(), "1"); !found || v != "value" {
		t.Fatal("loaded value not cached")
	}
}

func TestCancelledCallerDoesNotFailTheSharedLoad(t *testing.T) {
	c, _ := New(memoryStore().store, Options[string]{Name: "notes", TTL: time.Minute})
	release := make(chan struct{})
	loader := func(ctx context.Context) (string, []string, error) {
		select {
		case <-release:
			return "v", nil, nil
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
	}
	impatient, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := c.GetOrLoad(impatient, "k", loader); first <- err }()
	time.Sleep(10 * time.Millisecond)
	second := make(chan string, 1)
	go func() { v, _ := c.GetOrLoad(context.Background(), "k", loader); second <- v }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("impatient caller: %v", err)
	}
	close(release)
	if v := <-second; v != "v" {
		t.Fatalf("patient caller got %q after the first caller cancelled", v)
	}
}

type brokenStore struct{ Store }

func (brokenStore) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, errors.New("redis down")
}

func (brokenStore) Set(context.Context, string, []byte, time.Duration, ...string) error {
	return errors.New("redis down")
}

func TestStoreOutageFallsBackToLoader(t *testing.T) {
	c, _ := New[string](brokenStore{}, Options[string]{Name: "notes", TTL: time.Minute})
	v, err := c.GetOrLoad(context.Background(), "k", func(context.Context) (string, []string, error) { return "fresh", nil, nil })
	if err != nil || v != "fresh" {
		t.Fatalf("got %q %v", v, err)
	}
}

func TestLoaderErrorIsReturnedAndNotCached(t *testing.T) {
	c, _ := New(memoryStore().store, Options[string]{Name: "notes", TTL: time.Minute})
	boom := errors.New("not found")
	if _, err := c.GetOrLoad(context.Background(), "k", func(context.Context) (string, []string, error) { return "", nil, boom }); !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, found, _ := c.Get(context.Background(), "k"); found {
		t.Fatal("failed load was cached")
	}
}

func TestUndecodableEntryIsAMiss(t *testing.T) {
	store := memoryStore()
	_ = store.store.Set(context.Background(), "nums:k", []byte("not json"), time.Minute)
	c, _ := New(store.store, Options[int]{Name: "nums", TTL: time.Minute})
	v, err := c.GetOrLoad(context.Background(), "k", func(context.Context) (int, []string, error) { return 7, nil, nil })
	if err != nil || v != 7 {
		t.Fatalf("got %d %v", v, err)
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	for _, opts := range []Options[int]{{Name: "", TTL: time.Second}, {Name: "a:b", TTL: time.Second}, {Name: "ok", TTL: 0}} {
		if _, err := New(memoryStore().store, opts); !errors.Is(err, ErrInvalidCache) {
			t.Errorf("%+v accepted", opts)
		}
	}
}

func TestMetricsCountHitsMissesAndInvalidations(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	store := memoryStore()
	c, _ := New(store.store, Options[string]{Name: "titles", TTL: time.Minute, MeterProvider: mp})
	ctx := context.Background()
	load := func(context.Context) (string, []string, error) { return "t", []string{"note:1"}, nil }
	_, _ = c.GetOrLoad(ctx, "1", load) // miss
	_, _ = c.GetOrLoad(ctx, "1", load) // hit
	_, _ = c.GetOrLoad(ctx, "1", load) // hit
	inv, _ := NewInvalidator(store.store, mp)
	_, _ = inv.InvalidateTags(ctx, "note:1")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	totals := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, dp := range sum.DataPoints {
					totals[m.Name] += dp.Value
				}
			}
		}
	}
	want := map[string]int64{"skein.cache.hits": 2, "skein.cache.misses": 1, "skein.cache.invalidations": 1}
	for name, n := range want {
		if totals[name] != n {
			t.Errorf("%s = %d, want %d (all: %v)", name, totals[name], n, totals)
		}
	}
}
