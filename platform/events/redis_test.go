package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/genuinebnt/skein-go/platform/internal/testseed"
)

// miniredis runs in this process; it speaks RESP over a loopback socket,
// as the platform's other Redis tests do.
func newMiniredis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), PoolSize: 64})
	t.Cleanup(func() { _ = client.Close() })
	return mr, client
}

func openRedisBus(t *testing.T, client *redis.Client, topo Topology, name string) *Redis {
	t.Helper()
	r, err := NewRedis(context.Background(), RedisConfig{Client: client, PollInterval: 50 * time.Millisecond, ConsumerName: name})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Provision(context.Background(), topo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, s := range topo.Subscriptions {
			if err := r.CheckInvariants(context.Background(), s.Name); err != nil {
				t.Error(err)
			}
		}
	})
	return r
}

func TestContractRedisOnMiniredis(t *testing.T) {
	_, client := newMiniredis(t)
	runContract(t, contractBackend{
		minBackoff: 10 * time.Millisecond, maxBackoff: 20 * time.Millisecond,
		open: func(t *testing.T, topo Topology) Bus { return openRedisBus(t, client, topo, "contract") },
	})
}

func shortTopology(attempts int, minBackoff time.Duration, ackDeadline time.Duration) Topology {
	return NewTopologyBuilder().Policy(attempts, minBackoff, 4*minBackoff, ackDeadline).
		Topic("t").Subscription("s", "t").PlainSubscription("dl", "t.dead-letter").Build()
}

// subscribeFor runs Subscribe in the background and returns a stop
// function that cancels it and checks it returned nil.
func subscribeFor(t *testing.T, bus Subscriber, sub string, h Handler) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- bus.Subscribe(ctx, sub, h) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			if err := <-errc; err != nil {
				t.Errorf("Subscribe: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRedisHoldsBackLaterMessagesWithTheSameKeyUntilTheNackedOneIsAcked(t *testing.T) {
	_, client := newMiniredis(t)
	r := openRedisBus(t, client, shortTopology(10, 100*time.Millisecond, 10*time.Second), "c")
	// Same key, so same partition; "b" is chosen to share it, which makes
	// the test show that other keys flow past the held-back one.
	other := ""
	for i := 0; other == ""; i++ {
		if k := fmt.Sprintf("k%d", i); RedisPartition(k, "") == RedisPartition("a", "") {
			other = k
		}
	}
	for _, p := range []struct{ key, payload string }{{"a", "a1"}, {other, "b1"}, {"a", "a2"}, {other, "b2"}} {
		if err := r.Publish(context.Background(), "t", envelope(t, p.key, p.payload)); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var seen []string
	failA1 := true
	stop := subscribeFor(t, r, "s", func(_ context.Context, msg Message) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, string(msg.Payload))
		if string(msg.Payload) == "a1" && failA1 {
			failA1 = false
			return errors.New("transient")
		}
		return nil
	})
	waitFor(t, "all four acked", func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) == 5 })
	stop()
	if want := []string{"a1", "b1", "b2", "a1", "a2"}; !slices.Equal(seen, want) {
		t.Fatalf("saw %v, want %v: a2 waits behind a1 while the other key flows", seen, want)
	}
}

func TestRedisCountsAttemptsAndDeadLettersWithPubSubAttributes(t *testing.T) {
	_, client := newMiniredis(t)
	r := openRedisBus(t, client, shortTopology(3, 5*time.Millisecond, 10*time.Second), "c")
	poison := envelope(t, "n1", "poison")
	if err := r.Publish(context.Background(), "t", poison); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var attempts []int
	stopFailing := subscribeFor(t, r, "s", func(_ context.Context, msg Message) error {
		mu.Lock()
		defer mu.Unlock()
		attempts = append(attempts, msg.DeliveryAttempt)
		return errors.New("always fails")
	})
	dead := make(chan Message, 1)
	stopDead := subscribeFor(t, r, "dl", func(_ context.Context, msg Message) error { dead <- msg; return nil })
	var got Message
	select {
	case got = <-dead:
	case <-time.After(20 * time.Second):
		t.Fatal("no dead letter")
	}
	stopFailing()
	stopDead()
	if !slices.Equal(attempts, []int{1, 2, 3}) {
		t.Errorf("attempts %v, want [1 2 3]", attempts)
	}
	if got.ID != poison.ID || got.Attributes[AttrDeadLetterSubscription] != "s" || got.Attributes[AttrDeadLetterDeliveryCount] != "3" {
		t.Errorf("dead letter %+v", got.Envelope)
	}
}

func TestRedisWaitsTheBackoffBeforeRedelivering(t *testing.T) {
	_, client := newMiniredis(t)
	const backoff = 300 * time.Millisecond
	r := openRedisBus(t, client, shortTopology(10, backoff, 10*time.Second), "c")
	if err := r.Publish(context.Background(), "t", envelope(t, "", "x")); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var at []time.Time
	stop := subscribeFor(t, r, "s", func(context.Context, Message) error {
		mu.Lock()
		defer mu.Unlock()
		at = append(at, time.Now())
		if len(at) == 1 {
			return errors.New("transient")
		}
		return nil
	})
	waitFor(t, "redelivery", func() bool { mu.Lock(); defer mu.Unlock(); return len(at) == 2 })
	stop()
	if gap := at[1].Sub(at[0]); gap < backoff {
		t.Fatalf("redelivered after %v, before the %v backoff", gap, backoff)
	}
}

// A consumer that took a message and died leaves it pending and its lease
// held. Nobody else may deliver it until the lease lapses (the visibility
// timeout); then the next consumer claims and redelivers it.
func TestRedisRedeliversACrashedConsumersMessageAfterTheVisibilityTimeout(t *testing.T) {
	mr, client := newMiniredis(t)
	const deadline = 2 * time.Second
	r := openRedisBus(t, client, shortTopology(10, 10*time.Millisecond, deadline), "survivor")
	sent := envelope(t, "n1", "in flight")
	if err := r.Publish(context.Background(), "t", sent); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	p := RedisPartition("n1", sent.ID)
	// The crashed consumer: it leased the partition, read the entry and
	// counted the attempt, then vanished.
	if err := client.Set(ctx, redisLeaseKey("s", p), "crashed#1", deadline).Err(); err != nil {
		t.Fatal(err)
	}
	read, err := client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: "s", Consumer: "crashed", Streams: []string{RedisStreamKey("t", p), ">"}, Count: 1, Block: -1}).Result()
	if err != nil || len(read[0].Messages) != 1 {
		t.Fatalf("crashed consumer read %v, %v", read, err)
	}
	client.HIncrBy(ctx, redisAttemptsKey("s", p), read[0].Messages[0].ID, 1)

	got := make(chan Message, 1)
	stop := subscribeFor(t, r, "s", func(_ context.Context, msg Message) error { got <- msg; return nil })
	select {
	case msg := <-got:
		t.Fatalf("delivered %s while the crashed consumer's lease was live", msg.ID)
	case <-time.After(300 * time.Millisecond):
	}
	mr.FastForward(deadline)
	select {
	case msg := <-got:
		if msg.ID != sent.ID || msg.DeliveryAttempt != 2 {
			t.Fatalf("redelivered %s attempt %d, want %s attempt 2", msg.ID, msg.DeliveryAttempt, sent.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("not redelivered after the lease lapsed")
	}
	stop()
}

// TestRedisConsumersSharePartitionsAndKeepPerKeyOrder runs two consumers
// of one subscription, the second joining midway, under random nacks, and
// checks every message is acked with each key's acks in publish order.
func TestRedisConsumersSharePartitionsAndKeepPerKeyOrder(t *testing.T) {
	rnd := testseed.Rand(testseed.Seed(t))
	_, client := newMiniredis(t)
	topo := shortTopology(1000, time.Millisecond, 150*time.Millisecond)
	first := openRedisBus(t, client, topo, "first")
	second := openRedisBus(t, client, topo, "second")

	var mu sync.Mutex
	acked := map[string][]string{}
	ackedBy := map[string]int{}
	handler := func(name string) Handler {
		return func(_ context.Context, msg Message) error {
			mu.Lock()
			defer mu.Unlock()
			if rnd.IntN(4) == 0 {
				return errors.New("random failure")
			}
			acked[msg.OrderingKey] = append(acked[msg.OrderingKey], msg.ID)
			ackedBy[name]++
			return nil
		}
	}
	published := map[string][]string{}
	publish := func(n int) {
		for range n {
			mu.Lock()
			key := fmt.Sprintf("note-%d", rnd.IntN(12))
			mu.Unlock()
			e := envelope(t, key, "x")
			published[key] = append(published[key], e.ID)
			if err := first.Publish(context.Background(), "t", e); err != nil {
				t.Fatal(err)
			}
		}
	}
	total := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, ids := range acked {
			n += len(ids)
		}
		return n
	}
	stopFirst := subscribeFor(t, first, "s", handler("first"))
	publish(150)
	waitFor(t, "some progress", func() bool { return total() > 20 })
	stopSecond := subscribeFor(t, second, "s", handler("second"))
	publish(150)
	waitFor(t, "every message acked", func() bool { return total() == 300 })
	stopFirst()
	stopSecond()

	for key, ids := range published {
		if !slices.Equal(acked[key], ids) {
			t.Fatalf("key %s acked %v, want publish order %v", key, acked[key], ids)
		}
	}
	if ackedBy["second"] == 0 {
		t.Error("the second consumer was never given a partition")
	}
}

func TestRedisDeadLettersAMalformedEntryWithoutCallingTheHandler(t *testing.T) {
	_, client := newMiniredis(t)
	r := openRedisBus(t, client, shortTopology(2, time.Millisecond, 10*time.Second), "c")
	if err := client.XAdd(context.Background(), &redis.XAddArgs{Stream: RedisStreamKey("t", 0), Values: []any{"payload", "no id"}}).Err(); err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{}, 1)
	stop := subscribeFor(t, r, "s", func(context.Context, Message) error { called <- struct{}{}; return nil })
	waitFor(t, "the dead letter", func() bool {
		n := int64(0)
		for p := range RedisPartitions {
			n += client.XLen(context.Background(), RedisStreamKey("t.dead-letter", p)).Val()
		}
		return n == 1
	})
	stop()
	select {
	case <-called:
		t.Fatal("handler received an entry without an event id")
	default:
	}
}

func TestRedisPublishRejectsInvalidEnvelopesAndUnknownTopics(t *testing.T) {
	_, client := newMiniredis(t)
	r := openRedisBus(t, client, shortTopology(2, time.Millisecond, time.Second), "c")
	if err := r.Publish(context.Background(), "t", Envelope{Source: "x"}); !errors.Is(err, ErrInvalidEnvelope) {
		t.Errorf("no id: %v", err)
	}
	if err := r.Publish(context.Background(), "nope", envelope(t, "", "")); !errors.Is(err, ErrUnknownTopic) {
		t.Errorf("unknown topic: %v", err)
	}
}

func TestRedisProvisionIsIdempotent(t *testing.T) {
	_, client := newMiniredis(t)
	r := openRedisBus(t, client, DefaultTopology(), "c")
	if err := r.Provision(context.Background(), DefaultTopology()); err != nil {
		t.Fatalf("second provision: %v", err)
	}
	groups, err := client.XInfoGroups(context.Background(), RedisStreamKey("documents.events", 3)).Result()
	if err != nil || len(groups) != 4 {
		t.Fatalf("documents.events partition 3 has groups %v (%v), want the four subscriptions", groups, err)
	}
}

// redisConformance is testdata/conformance/events/redis-wire.json: the
// partition of sample keys, shared with the Python backend and the Rust
// port.
type redisConformance struct {
	Why        string `json:"why"`
	Partitions int    `json:"partitions"`
	Cases      []struct {
		OrderingKey string `json:"ordering_key"`
		ID          string `json:"id"`
		Partition   int    `json:"partition"`
	} `json:"cases"`
}

func TestRedisPartitionMatchesTheConformanceFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/conformance/events/redis-wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx redisConformance
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if fx.Partitions != RedisPartitions || len(fx.Cases) == 0 {
		t.Fatalf("fixture has %d partitions and %d cases", fx.Partitions, len(fx.Cases))
	}
	for _, c := range fx.Cases {
		if got := RedisPartition(c.OrderingKey, c.ID); got != c.Partition {
			t.Errorf("partition(%q, %q) = %d, fixture says %d", c.OrderingKey, c.ID, got, c.Partition)
		}
	}
}

func FuzzRedisFieldsRoundTrip(f *testing.F) {
	f.Add("id-1", "documents", "n1", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "kind", "note_created", []byte("p"))
	f.Add("x", "y", "", "", "", "", []byte{0, 255})
	f.Fuzz(func(t *testing.T, id, source, key, traceparent, attrKey, attrValue string, payload []byte) {
		e := Envelope{ID: id, Source: source, OrderingKey: key, Traceparent: traceparent, Payload: payload, Attributes: map[string]string{}}
		if attrKey != "" {
			e.Attributes[attrKey] = attrValue
		}
		if e.Validate() != nil {
			return
		}
		fields := toRedisFields(e)
		values := map[string]any{}
		for i := 0; i < len(fields); i += 2 {
			values[fields[i].(string)] = fields[i+1]
		}
		got, err := fromRedisFields(values)
		if err != nil {
			t.Fatalf("valid envelope did not round-trip: %v", err)
		}
		if got.ID != e.ID || got.Source != e.Source || got.OrderingKey != e.OrderingKey || got.Traceparent != e.Traceparent ||
			string(got.Payload) != string(e.Payload) || len(got.Attributes) != len(e.Attributes) || got.Attributes[attrKey] != e.Attributes[attrKey] {
			t.Fatalf("round trip changed the envelope:\n got %+v\nwant %+v", got, e)
		}
	})
}
