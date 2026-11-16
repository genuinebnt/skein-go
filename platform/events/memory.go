package events

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// DefaultMaxDeliveryAttempts matches the local Pub/Sub setup.
const DefaultMaxDeliveryAttempts = 10

// MemoryOptions configure NewMemory.
type MemoryOptions struct {
	Logger         *slog.Logger
	TracerProvider trace.TracerProvider
	Now            func() time.Time
}

// Memory is an in-process broker with Pub/Sub's semantics: topics fan out
// to subscriptions, delivery is at least once, a nack redelivers, ordering
// keys hold back later messages behind a nacked one, and a message is
// dead-lettered after its subscription's maximum attempts.
//
// Deliver runs one synchronous pass, which makes consumer tests
// deterministic; Subscribe loops over passes like a real subscriber.
type Memory struct {
	dispatch dispatcher
	tracer   trace.Tracer
	now      func() time.Time

	mu        sync.Mutex
	topics    map[string][]string // topic -> subscriptions
	subs      map[string]*memorySubscription
	published map[string][]Envelope
	wake      chan struct{} // closed and replaced on every publish
}

type memorySubscription struct {
	topic       string
	maxAttempts int
	queue       []*Message
	dead        []Message
	delivering  sync.Mutex // one pass at a time per subscription
}

// NewMemory returns an empty broker.
func NewMemory(opts MemoryOptions) *Memory {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	tp := opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return &Memory{
		dispatch:  newDispatcher(tp, opts.Logger, "memory"),
		tracer:    tp.Tracer(instrumentation),
		now:       opts.Now,
		topics:    map[string][]string{},
		subs:      map[string]*memorySubscription{},
		published: map[string][]Envelope{},
		wake:      make(chan struct{}),
	}
}

// CreateTopic makes a topic exist. Creating an existing topic is a no-op.
func (m *Memory) CreateTopic(topic string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.topics[topic]; !ok {
		m.topics[topic] = nil
	}
}

// CreateSubscription attaches a subscription to a topic, creating the
// topic if needed. maxAttempts <= 0 means DefaultMaxDeliveryAttempts. Like
// Pub/Sub, a new subscription sees only messages published after it.
func (m *Memory) CreateSubscription(name, topic string, maxAttempts int) error {
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxDeliveryAttempts
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.subs[name]; exists {
		return fmt.Errorf("events: subscription %q already exists", name)
	}
	m.topics[topic] = append(m.topics[topic], name)
	m.subs[name] = &memorySubscription{topic: topic, maxAttempts: maxAttempts}
	return nil
}

// Publish implements Publisher.
func (m *Memory) Publish(ctx context.Context, topic string, e Envelope) error {
	if err := e.Validate(); err != nil {
		return err
	}
	ctx, span := publishSpan(ctx, m.tracer, "memory", topic, e)
	defer span.End()
	InjectTrace(ctx, &e)

	m.mu.Lock()
	defer m.mu.Unlock()
	subs, ok := m.topics[topic]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownTopic, topic)
	}
	m.published[topic] = append(m.published[topic], e.Clone())
	now := m.now()
	for _, name := range subs {
		s := m.subs[name]
		s.queue = append(s.queue, &Message{Envelope: e.Clone(), PublishTime: now})
	}
	close(m.wake)
	m.wake = make(chan struct{})
	return nil
}

// Deliver runs one pass over the subscription's queue, in publish order,
// and returns how many messages were acked. Within a pass, a message whose
// ordering key already had a nack is skipped, so per-key order holds.
func (m *Memory) Deliver(ctx context.Context, subscription string, h Handler) (int, error) {
	m.mu.Lock()
	s, ok := m.subs[subscription]
	m.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownSubscription, subscription)
	}
	s.delivering.Lock()
	defer s.delivering.Unlock()

	m.mu.Lock()
	batch := append([]*Message(nil), s.queue...)
	m.mu.Unlock()

	blocked := map[string]bool{}
	done := map[*Message]bool{}
	var dead []Message
	acked := 0
	for _, msg := range batch {
		if ctx.Err() != nil {
			break
		}
		if msg.OrderingKey != "" && blocked[msg.OrderingKey] {
			continue
		}
		msg.DeliveryAttempt++
		delivered := *msg
		delivered.Envelope = msg.Envelope.Clone()
		if m.dispatch.dispatch(ctx, subscription, h, delivered) {
			done[msg] = true
			acked++
			continue
		}
		if msg.DeliveryAttempt >= s.maxAttempts {
			done[msg] = true
			dead = append(dead, delivered)
			continue
		}
		if msg.OrderingKey != "" {
			blocked[msg.OrderingKey] = true
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	kept := s.queue[:0]
	for _, msg := range s.queue {
		if !done[msg] {
			kept = append(kept, msg)
		}
	}
	s.queue = kept
	s.dead = append(s.dead, dead...)
	return acked, nil
}

// Subscribe implements Subscriber: it runs passes until ctx is cancelled,
// waiting for a publish when the queue is empty and retrying nacked
// messages after retryDelay.
func (m *Memory) Subscribe(ctx context.Context, subscription string, h Handler) error {
	const retryDelay = 10 * time.Millisecond
	for {
		m.mu.Lock()
		wake := m.wake
		m.mu.Unlock()
		if _, err := m.Deliver(ctx, subscription, h); err != nil {
			return err
		}
		var retry <-chan time.Time
		if m.Pending(subscription) > 0 {
			retry = time.After(retryDelay)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-retry:
		}
	}
}

// Pending returns how many messages wait in the subscription.
func (m *Memory) Pending(subscription string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.subs[subscription]; ok {
		return len(s.queue)
	}
	return 0
}

// DeadLetters returns the messages the subscription gave up on.
func (m *Memory) DeadLetters(subscription string) []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.subs[subscription]; ok {
		return append([]Message(nil), s.dead...)
	}
	return nil
}

// Published returns every envelope published to topic, in order, for
// assertions in tests.
func (m *Memory) Published(topic string) []Envelope {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Envelope(nil), m.published[topic]...)
}

// CheckInvariants verifies that every subscription belongs to a topic that
// lists it, queued messages have attempts left, and dead letters used all
// of theirs.
func (m *Memory) CheckInvariants() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, s := range m.subs {
		listed := false
		for _, n := range m.topics[s.topic] {
			listed = listed || n == name
		}
		if !listed {
			return fmt.Errorf("events: invariant: subscription %q not listed by topic %q", name, s.topic)
		}
		for _, msg := range s.queue {
			if msg.DeliveryAttempt >= s.maxAttempts {
				return fmt.Errorf("events: invariant: %q queues %s after %d attempts", name, msg.ID, msg.DeliveryAttempt)
			}
		}
		for _, msg := range s.dead {
			if msg.DeliveryAttempt != s.maxAttempts {
				return fmt.Errorf("events: invariant: %q dead-lettered %s after %d attempts", name, msg.ID, msg.DeliveryAttempt)
			}
		}
	}
	return nil
}
