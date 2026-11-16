package events

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"cloud.google.com/go/pubsub/v2/pstest"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/genuinebnt/skein-go/platform/internal/testseed"
)

func envelope(t testing.TB, key, payload string) Envelope {
	t.Helper()
	e, err := NewEnvelope(context.Background(), "documents", key, []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func newMemory(t *testing.T) *Memory {
	t.Helper()
	m := NewMemory(MemoryOptions{})
	t.Cleanup(func() {
		if err := m.CheckInvariants(); err != nil {
			t.Error(err)
		}
	})
	return m
}

func mustDeliver(t *testing.T, m *Memory, sub string, h Handler) int {
	t.Helper()
	n, err := m.Deliver(context.Background(), sub, h)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	return n
}

func ack(context.Context, Message) error { return nil }

func TestEnvelopeValidation(t *testing.T) {
	ok := envelope(t, "", "x")
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]Envelope{
		"no id":         {Source: "documents"},
		"no source":     {ID: "1"},
		"reserved attr": {ID: "1", Source: "s", Attributes: map[string]string{AttrEventID: "x"}},
		"goog prefix":   {ID: "1", Source: "s", Attributes: map[string]string{"googclient_x": "x"}},
	} {
		if err := e.Validate(); !errors.Is(err, ErrInvalidEnvelope) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMemoryFansOutToEverySubscription(t *testing.T) {
	m := newMemory(t)
	_ = m.CreateSubscription("search.documents-events", "documents.events", 0)
	_ = m.CreateSubscription("ml.documents-events", "documents.events", 0)
	if err := m.Publish(context.Background(), "documents.events", envelope(t, "n1", "a")); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"search.documents-events", "ml.documents-events"} {
		if n := mustDeliver(t, m, sub, ack); n != 1 {
			t.Errorf("%s acked %d", sub, n)
		}
		if m.Pending(sub) != 0 {
			t.Errorf("%s still has messages", sub)
		}
	}
	if err := m.Publish(context.Background(), "nope", envelope(t, "", "")); !errors.Is(err, ErrUnknownTopic) {
		t.Errorf("unknown topic: %v", err)
	}
	if _, err := m.Deliver(context.Background(), "nope", ack); !errors.Is(err, ErrUnknownSubscription) {
		t.Errorf("unknown subscription: %v", err)
	}
}

func TestMemoryRedeliversNackedAndDeadLettersAfterMaxAttempts(t *testing.T) {
	m := newMemory(t)
	_ = m.CreateSubscription("s", "t", 3)
	_ = m.Publish(context.Background(), "t", envelope(t, "", "poison"))
	var attempts []int
	fail := func(_ context.Context, msg Message) error {
		attempts = append(attempts, msg.DeliveryAttempt)
		return errors.New("cannot handle")
	}
	for range 5 {
		mustDeliver(t, m, "s", fail)
	}
	if !slices.Equal(attempts, []int{1, 2, 3}) {
		t.Errorf("attempts %v, want 1, 2, 3 then dead-lettered", attempts)
	}
	dead := m.DeadLetters("s")
	if len(dead) != 1 || string(dead[0].Payload) != "poison" || m.Pending("s") != 0 {
		t.Fatalf("dead letters %v, pending %d", dead, m.Pending("s"))
	}
}

func TestMemoryPanickingHandlerNacks(t *testing.T) {
	m := newMemory(t)
	_ = m.CreateSubscription("s", "t", 0)
	_ = m.Publish(context.Background(), "t", envelope(t, "", "x"))
	if n := mustDeliver(t, m, "s", func(context.Context, Message) error { panic("bug") }); n != 0 || m.Pending("s") != 1 {
		t.Fatalf("acked %d, pending %d", n, m.Pending("s"))
	}
}

func TestMemoryHoldsBackLaterMessagesWithTheSameKey(t *testing.T) {
	m := newMemory(t)
	_ = m.CreateSubscription("s", "t", 0)
	for _, p := range []string{"a1", "b1", "a2", "b2"} {
		_ = m.Publish(context.Background(), "t", envelope(t, p[:1], p))
	}
	var seen []string
	failA1 := true
	h := func(_ context.Context, msg Message) error {
		seen = append(seen, string(msg.Payload))
		if string(msg.Payload) == "a1" && failA1 {
			return errors.New("transient")
		}
		return nil
	}
	mustDeliver(t, m, "s", h)
	if !slices.Equal(seen, []string{"a1", "b1", "b2"}) {
		t.Fatalf("first pass saw %v: a2 must wait behind a1, b must flow", seen)
	}
	failA1, seen = false, nil
	mustDeliver(t, m, "s", h)
	if !slices.Equal(seen, []string{"a1", "a2"}) {
		t.Fatalf("second pass saw %v", seen)
	}
}

// TestMemoryKeepsPerKeyOrderUnderRandomFailures checks, for random
// publishes and random nacks, that each key's acks happen in publish order
// and every message ends acked exactly once or dead-lettered.
func TestMemoryKeepsPerKeyOrderUnderRandomFailures(t *testing.T) {
	r := testseed.Rand(testseed.Seed(t))
	m := newMemory(t)
	_ = m.CreateSubscription("s", "t", 4)
	keys := []string{"", "n1", "n2", "n3"}
	published := map[string][]string{}
	for i := range 300 {
		key := keys[r.IntN(len(keys))]
		e := envelope(t, key, fmt.Sprint(i))
		published[key] = append(published[key], e.ID)
		_ = m.Publish(context.Background(), "t", e)
	}
	acked := map[string][]string{}
	h := func(_ context.Context, msg Message) error {
		if r.IntN(3) == 0 {
			return errors.New("random failure")
		}
		acked[msg.OrderingKey] = append(acked[msg.OrderingKey], msg.ID)
		return nil
	}
	for passes := 0; m.Pending("s") > 0; passes++ {
		if passes > 1000 {
			t.Fatal("queue never drained")
		}
		mustDeliver(t, m, "s", h)
	}
	dead := map[string]bool{}
	for _, msg := range m.DeadLetters("s") {
		dead[msg.ID] = true
	}
	for _, key := range keys {
		var survivors []string
		for _, id := range published[key] {
			if !dead[id] {
				survivors = append(survivors, id)
			}
		}
		if key != "" && !slices.Equal(acked[key], survivors) {
			t.Fatalf("key %q: acked %v, want publish order %v", key, acked[key], survivors)
		}
		if key == "" && len(acked[key]) != len(survivors) {
			t.Fatalf("unordered: acked %d, want %d", len(acked[key]), len(survivors))
		}
	}
}

func TestDeliveryContinuesThePublishersTrace(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	m := NewMemory(MemoryOptions{TracerProvider: tp})
	_ = m.CreateSubscription("s", "t", 0)

	ctx, request := tp.Tracer("t").Start(context.Background(), "PUT /v1/notes/1")
	// Made outside the request, so Publish must fill in the traceparent.
	e := Envelope{ID: "e1", Source: "documents", OrderingKey: "n1"}
	if err := m.Publish(ctx, "t", e); err != nil {
		t.Fatal(err)
	}
	request.End()
	var handlerTraceID string
	mustDeliver(t, m, "s", func(ctx context.Context, _ Message) error {
		handlerTraceID = trace.SpanContextFromContext(ctx).TraceID().String()
		return nil
	})

	byName := map[string]tracetest.SpanStub{}
	for _, s := range spans.GetSpans() {
		byName[s.Name] = s
	}
	send, process := byName["t send"], byName["s process"]
	traceID := request.SpanContext().TraceID()
	if send.SpanContext.TraceID() != traceID || process.SpanContext.TraceID() != traceID || handlerTraceID != traceID.String() {
		t.Fatalf("trace not continued: request %s send %s process %s handler %s",
			traceID, send.SpanContext.TraceID(), process.SpanContext.TraceID(), handlerTraceID)
	}
	if process.Parent.SpanID() != send.SpanContext.SpanID() || process.SpanKind != trace.SpanKindConsumer {
		t.Fatalf("consumer span parent %s, want the producer span %s", process.Parent.SpanID(), send.SpanContext.SpanID())
	}
}

func TestPublishedEnvelopesAreIsolatedFromTheCaller(t *testing.T) {
	m := newMemory(t)
	_ = m.CreateSubscription("s", "t", 0)
	e := envelope(t, "", "original")
	e.Attributes = map[string]string{"kind": "note_created"}
	_ = m.Publish(context.Background(), "t", e)
	e.Payload[0] = 'X'
	e.Attributes["kind"] = "changed"
	mustDeliver(t, m, "s", func(_ context.Context, msg Message) error {
		if string(msg.Payload) != "original" || msg.Attributes["kind"] != "note_created" {
			t.Errorf("stored message changed through the caller's references: %+v", msg)
		}
		return nil
	})
}

func TestMemorySubscribeDeliversUntilCancelled(t *testing.T) {
	m := newMemory(t)
	_ = m.CreateSubscription("s", "t", 0)
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan string, 2)
	done := make(chan error, 1)
	go func() {
		done <- m.Subscribe(ctx, "s", func(_ context.Context, msg Message) error {
			got <- string(msg.Payload)
			return nil
		})
	}()
	_ = m.Publish(context.Background(), "t", envelope(t, "", "one"))
	_ = m.Publish(context.Background(), "t", envelope(t, "", "two"))
	for _, want := range []string{"one", "two"} {
		select {
		case v := <-got:
			if v != want {
				t.Fatalf("got %q, want %q", v, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("message not delivered")
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func FuzzAttributesRoundTrip(f *testing.F) {
	f.Add("id-1", "documents", "n1", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "kind", "note_created")
	f.Add("x", "y", "", "", "", "")
	f.Fuzz(func(t *testing.T, id, source, key, traceparent, attrKey, attrValue string) {
		e := Envelope{ID: id, Source: source, OrderingKey: key, Traceparent: traceparent, Payload: []byte("p"), Attributes: map[string]string{}}
		if attrKey != "" {
			e.Attributes[attrKey] = attrValue
		}
		if e.Validate() != nil {
			return
		}
		got, err := fromAttributes(toAttributes(e), e.OrderingKey, e.Payload)
		if err != nil {
			t.Fatalf("valid envelope did not round-trip: %v", err)
		}
		if got.ID != e.ID || got.Source != e.Source || got.Traceparent != e.Traceparent || !maps.Equal(got.Attributes, e.Attributes) {
			t.Fatalf("round trip changed the envelope:\n got %+v\nwant %+v", got, e)
		}
	})
}

// pstestFixture runs Google's in-process Pub/Sub fake. It listens on a
// loopback port inside this process; nothing leaves the machine.
func pstestFixture(t *testing.T) (*pstest.Server, *PubSub) {
	t.Helper()
	srv := pstest.NewServer()
	t.Cleanup(func() { _ = srv.Close() })
	ps, err := NewPubSub(context.Background(), PubSubConfig{
		Project: "skein-test",
		ClientOptions: []option.ClientOption{
			option.WithEndpoint(srv.Addr),
			option.WithoutAuthentication(),
			option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	ctx := context.Background()
	if _, err := ps.client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: "projects/skein-test/topics/documents.events"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:                  "projects/skein-test/subscriptions/search.documents-events",
		Topic:                 "projects/skein-test/topics/documents.events",
		EnableMessageOrdering: true,
		AckDeadlineSeconds:    10,
	}); err != nil {
		t.Fatal(err)
	}
	return srv, ps
}

func TestPubSubRoundTripsEnvelopes(t *testing.T) {
	_, ps := pstestFixture(t)
	ctx := context.Background()
	sent := envelope(t, "n1", "payload-bytes")
	sent.Traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	sent.Attributes = map[string]string{"kind": "blocks_changed"}
	if err := ps.Publish(ctx, "documents.events", sent); err != nil {
		t.Fatal(err)
	}

	recvCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var got Message
	var once sync.Once
	err := ps.Subscribe(recvCtx, "search.documents-events", func(_ context.Context, msg Message) error {
		once.Do(func() { got = msg; cancel() })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := sent.Clone()
	got.Envelope.Attributes = maps.Clone(got.Envelope.Attributes)
	if !reflect.DeepEqual(got.Envelope, want) {
		t.Fatalf("got %+v\nwant %+v", got.Envelope, want)
	}
	if got.PublishTime.IsZero() {
		t.Error("publish time missing")
	}
}

// Redelivery after a nack is not unit-tested against pstest: the fake's
// redelivery timing proved unreliable (a nacked message was sometimes not
// redelivered within 10 seconds). Memory's tests pin the semantics, and
// TestEmulatorDeadLettersAfterMaxAttempts checks them on the real emulator,
// where dead-lettering requires every redelivery to happen.

func TestPubSubMalformedMessageIsNackedNotHandled(t *testing.T) {
	srv, ps := pstestFixture(t)
	srv.Publish("projects/skein-test/topics/documents.events", []byte("raw"), map[string]string{"not": "an envelope"})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	handled := false
	_ = ps.Subscribe(ctx, "search.documents-events", func(context.Context, Message) error { handled = true; return nil })
	if handled {
		t.Fatal("handler received a message without an event id")
	}
	if msgs := srv.Messages(); len(msgs) != 1 || msgs[0].Acks != 0 {
		t.Fatalf("malformed message must stay unacked for the dead-letter topic: %+v", msgs)
	}
}

func TestPubSubUnknownTopicIsReported(t *testing.T) {
	_, ps := pstestFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ps.Publish(ctx, "missing", envelope(t, "", "x")); !errors.Is(err, ErrUnknownTopic) {
		t.Fatalf("got %v", err)
	}
}
