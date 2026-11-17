package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// The contract suite: behaviour every backend must share, so a service
// cannot tell Memory, Redis and Pub/Sub apart. Each backend's harness
// (contractMemory and contractRedis here; the real Redis server and the
// Pub/Sub emulator in integration_test.go) provisions a fresh topology and
// runs runContract. Timing is never asserted, only outcomes within a
// generous deadline, because the brokers' clocks differ.

// contractBackend opens a bus with the topology already provisioned.
type contractBackend struct {
	open func(t *testing.T, topo Topology) Bus
	// minBackoff and maxBackoff are the redelivery policy the topology
	// uses; Pub/Sub needs whole seconds, the local backends do not.
	minBackoff, maxBackoff time.Duration
}

// contractNames is one test's topology, prefixed so runs against a shared
// broker never meet.
type contractNames struct {
	topic, deadLetters, subA, subB, deadLetterSub string
}

const contractAttempts = 5 // Pub/Sub's minimum for a dead-letter policy

func newContractTopology(t *testing.T, b contractBackend) (Topology, contractNames) {
	t.Helper()
	var raw [4]byte
	_, _ = rand.Read(raw[:])
	p := "ct" + hex.EncodeToString(raw[:]) + "-"
	n := contractNames{topic: p + "t", deadLetters: p + "t" + DeadLetterSuffix, subA: p + "a", subB: p + "b", deadLetterSub: p + "dl"}
	topo := NewTopologyBuilder().
		Policy(contractAttempts, b.minBackoff, b.maxBackoff, 10*time.Second).
		Topic(n.topic).
		Subscription(n.subA, n.topic).
		Subscription(n.subB, n.topic).
		PlainSubscription(n.deadLetterSub, n.deadLetters).
		Build()
	return topo, n
}

// consume runs Subscribe until done reports true (checked after every
// handler call) or the deadline passes, and checks Subscribe returns nil.
func consume(t *testing.T, bus Bus, sub string, h Handler, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var finished bool
	var mu sync.Mutex
	errc := make(chan error, 1)
	go func() {
		errc <- bus.Subscribe(ctx, sub, func(ctx context.Context, msg Message) error {
			err := h(ctx, msg)
			if done() {
				mu.Lock()
				finished = true
				mu.Unlock()
				cancel()
			}
			return err
		})
	}()
	if err := <-errc; err != nil {
		t.Fatalf("Subscribe(%s): %v", sub, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !finished {
		t.Fatalf("Subscribe(%s): gave up waiting", sub)
	}
}

func runContract(t *testing.T, b contractBackend) {
	t.Run("RoundTripsEveryEnvelopeField", func(t *testing.T) { contractRoundTrip(t, b) })
	t.Run("FansOutToEverySubscription", func(t *testing.T) { contractFanOut(t, b) })
	t.Run("RedeliversANackedMessage", func(t *testing.T) { contractRedelivers(t, b) })
	t.Run("DeadLettersAfterMaxAttempts", func(t *testing.T) { contractDeadLetters(t, b) })
	t.Run("KeepsPerKeyOrderAcrossNacks", func(t *testing.T) { contractOrder(t, b) })
	t.Run("ReportsUnknownNames", func(t *testing.T) { contractUnknown(t, b) })
	t.Run("ContinuesThePublishersTrace", func(t *testing.T) { contractTrace(t, b) })
}

func contractRoundTrip(t *testing.T, b contractBackend) {
	topo, n := newContractTopology(t, b)
	bus := b.open(t, topo)
	sent := envelope(t, "note-1", "payload \x00 bytes \xff")
	sent.Traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	sent.Attributes = map[string]string{"kind": "blocks_changed", "empty": ""}
	if err := bus.Publish(context.Background(), n.topic, sent); err != nil {
		t.Fatal(err)
	}
	var got Message
	consume(t, bus, n.subA, func(_ context.Context, msg Message) error { got = msg; return nil }, func() bool { return true })
	want := sent.Clone()
	if got.ID != want.ID || got.Source != want.Source || got.OrderingKey != want.OrderingKey ||
		got.Traceparent != want.Traceparent || string(got.Payload) != string(want.Payload) || !maps.Equal(got.Attributes, want.Attributes) {
		t.Fatalf("got %+v\nwant %+v", got.Envelope, want)
	}
	if got.DeliveryAttempt != 1 || got.PublishTime.IsZero() {
		t.Errorf("attempt %d, publish time %v; want 1 and a time", got.DeliveryAttempt, got.PublishTime)
	}
}

func contractFanOut(t *testing.T, b contractBackend) {
	topo, n := newContractTopology(t, b)
	bus := b.open(t, topo)
	sent := envelope(t, "", "x")
	if err := bus.Publish(context.Background(), n.topic, sent); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{n.subA, n.subB} {
		var got string
		consume(t, bus, sub, func(_ context.Context, msg Message) error { got = msg.ID; return nil }, func() bool { return true })
		if got != sent.ID {
			t.Errorf("%s got %q, want %q", sub, got, sent.ID)
		}
	}
}

func contractRedelivers(t *testing.T, b contractBackend) {
	topo, n := newContractTopology(t, b)
	bus := b.open(t, topo)
	sent := envelope(t, "note-1", "retry me")
	if err := bus.Publish(context.Background(), n.topic, sent); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var attempts []int
	consume(t, bus, n.subA, func(_ context.Context, msg Message) error {
		mu.Lock()
		defer mu.Unlock()
		if msg.ID != sent.ID {
			return fmt.Errorf("unexpected %s", msg.ID)
		}
		attempts = append(attempts, msg.DeliveryAttempt)
		if len(attempts) == 1 {
			return errors.New("transient")
		}
		return nil
	}, func() bool { mu.Lock(); defer mu.Unlock(); return len(attempts) >= 2 })
	if !slices.Equal(attempts, []int{1, 2}) {
		t.Errorf("attempts %v, want [1 2]", attempts)
	}
}

func contractDeadLetters(t *testing.T, b contractBackend) {
	topo, n := newContractTopology(t, b)
	bus := b.open(t, topo)
	poison := envelope(t, "note-1", "poison")
	if err := bus.Publish(context.Background(), n.topic, poison); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failing := make(chan error, 1)
	go func() {
		failing <- bus.Subscribe(ctx, n.subA, func(context.Context, Message) error { return errors.New("always fails") })
	}()
	var dead Message
	consume(t, bus, n.deadLetterSub, func(_ context.Context, msg Message) error { dead = msg; return nil }, func() bool { return true })
	cancel()
	if err := <-failing; err != nil {
		t.Fatal(err)
	}
	if dead.ID != poison.ID || string(dead.Payload) != "poison" || dead.OrderingKey != "note-1" {
		t.Fatalf("dead letter %+v, want the poison envelope", dead.Envelope)
	}
}

// contractOrder nacks one message of key "a" once and checks that each
// key's messages are first delivered in publish order and all end up
// acked. That is all three backends share: Memory and Redis also hold the
// key's later messages back until the nacked one is acked (their own
// tests pin that), while the Pub/Sub emulator delivers them during the
// nacked message's backoff (docs/local-first.md, "Ordering").
func contractOrder(t *testing.T, b contractBackend) {
	topo, n := newContractTopology(t, b)
	bus := b.open(t, topo)
	var order = map[string][]string{}
	for i, key := range []string{"a", "b", "a", "a", "b", "a", "b"} {
		e := envelope(t, key, fmt.Sprintf("%s%d", key, i))
		order[key] = append(order[key], e.ID)
		if err := bus.Publish(context.Background(), n.topic, e); err != nil {
			t.Fatal(err)
		}
	}
	nackOnce := order["a"][1]
	var mu sync.Mutex
	firstSeen := map[string][]string{}
	seen, acked := map[string]bool{}, map[string]bool{}
	consume(t, bus, n.subA, func(_ context.Context, msg Message) error {
		mu.Lock()
		defer mu.Unlock()
		if !seen[msg.ID] {
			seen[msg.ID] = true
			firstSeen[msg.OrderingKey] = append(firstSeen[msg.OrderingKey], msg.ID)
		}
		if msg.ID == nackOnce && msg.DeliveryAttempt == 1 {
			return errors.New("transient")
		}
		acked[msg.ID] = true
		return nil
	}, func() bool { mu.Lock(); defer mu.Unlock(); return len(acked) == 7 })
	for key, ids := range order {
		if !slices.Equal(firstSeen[key], ids) {
			t.Errorf("key %s first delivered as %v, want publish order %v", key, firstSeen[key], ids)
		}
	}
}

func contractUnknown(t *testing.T, b contractBackend) {
	topo, n := newContractTopology(t, b)
	bus := b.open(t, topo)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := bus.Publish(ctx, n.topic+"-missing", envelope(t, "", "x")); !errors.Is(err, ErrUnknownTopic) {
		t.Errorf("publish to a missing topic: %v", err)
	}
	if err := bus.Subscribe(ctx, n.subA+"-missing", ack); !errors.Is(err, ErrUnknownSubscription) {
		t.Errorf("subscribe to a missing subscription: %v", err)
	}
}

func contractTrace(t *testing.T, b contractBackend) {
	topo, n := newContractTopology(t, b)
	bus := b.open(t, topo)
	tp := sdktrace.NewTracerProvider()
	ctx, request := tp.Tracer("test").Start(context.Background(), "PUT /v1/notes/1")
	e := Envelope{ID: envelope(t, "", "").ID, Source: "documents", OrderingKey: "n1"}
	if err := bus.Publish(ctx, n.topic, e); err != nil {
		t.Fatal(err)
	}
	request.End()
	var handlerTrace trace.TraceID
	consume(t, bus, n.subA, func(ctx context.Context, _ Message) error {
		handlerTrace = trace.SpanContextFromContext(ctx).TraceID()
		return nil
	}, func() bool { return true })
	if handlerTrace != request.SpanContext().TraceID() {
		t.Fatalf("handler trace %s, want the publisher's %s", handlerTrace, request.SpanContext().TraceID())
	}
}

func TestContractMemory(t *testing.T) {
	runContract(t, contractBackend{
		minBackoff: 10 * time.Millisecond, maxBackoff: 20 * time.Millisecond,
		open: func(t *testing.T, topo Topology) Bus {
			m := NewMemory(MemoryOptions{})
			if err := m.Provision(context.Background(), topo); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := m.CheckInvariants(); err != nil {
					t.Error(err)
				}
			})
			return m
		},
	})
}
