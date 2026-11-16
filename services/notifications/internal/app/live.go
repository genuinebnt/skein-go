package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

// ErrSlowSubscriber ends a subscription whose client did not keep up. The
// client should resubscribe and reload its inbox; dropping the stream is
// better than silently skipping notifications.
var ErrSlowSubscriber = errors.New("subscriber fell behind")

// DefaultSubscriberBuffer is how many notifications may wait for one slow
// subscriber before it is dropped.
const DefaultSubscriberBuffer = 64

// Hub is this replica's registry of live subscribers. Producers on any
// replica publish through the Fanout; the Fanout calls Deliver on every
// replica that joined the recipient, and Deliver hands the notification to
// that replica's local subscribers without blocking.
//
// Invariant (CheckInvariants): the users joined on the Fanout are exactly
// the users with at least one local subscriber.
type Hub struct {
	fanout Fanout
	buffer int
	logger *slog.Logger

	mu     sync.Mutex
	byUser map[string][]*Subscription
	joined map[string]bool

	active  metric.Int64UpDownCounter
	dropped metric.Int64Counter
}

// Subscription is one open Subscribe stream.
type Subscription struct {
	hub    *Hub
	userID string
	ch     chan domain.Notification
	done   chan struct{}
	once   sync.Once
	err    error
}

// NewHub returns an empty hub. buffer <= 0 means DefaultSubscriberBuffer.
func NewHub(fanout Fanout, buffer int, logger *slog.Logger, mp metric.MeterProvider) (*Hub, error) {
	if buffer <= 0 {
		buffer = DefaultSubscriberBuffer
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	meter := mp.Meter("github.com/genuinebnt/skein-go/services/notifications/internal/app")
	active, err1 := meter.Int64UpDownCounter("skein.notifications.live.subscribers", metric.WithUnit("{subscriber}"),
		metric.WithDescription("Open Subscribe streams on this replica."))
	dropped, err2 := meter.Int64Counter("skein.notifications.live.dropped", metric.WithUnit("{subscriber}"),
		metric.WithDescription("Subscribers dropped for falling behind."))
	if err := errors.Join(err1, err2); err != nil {
		return nil, fmt.Errorf("app: metrics: %w", err)
	}
	return &Hub{fanout: fanout, buffer: buffer, logger: logger, byUser: map[string][]*Subscription{}, joined: map[string]bool{},
		active: active, dropped: dropped}, nil
}

// Subscribe registers a subscriber for userID. The first local subscriber
// for a user joins the fan-out; if that fails, nothing is registered.
func (h *Hub) Subscribe(ctx context.Context, userID string) (*Subscription, error) {
	s := &Subscription{hub: h, userID: userID, ch: make(chan domain.Notification, h.buffer), done: make(chan struct{})}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.joined[userID] {
		// Joining under the lock keeps join/leave for one user in order; it
		// is a single non-blocking Redis command.
		if err := h.fanout.Join(ctx, userID); err != nil {
			return nil, fmt.Errorf("app: join live fan-out: %w", err)
		}
		h.joined[userID] = true
	}
	h.byUser[userID] = append(h.byUser[userID], s)
	h.active.Add(ctx, 1)
	return s, nil
}

// Deliver hands n to every local subscriber of its recipient. A subscriber
// whose buffer is full is closed with ErrSlowSubscriber.
func (h *Hub) Deliver(n domain.Notification) {
	h.mu.Lock()
	subs := slices.Clone(h.byUser[n.RecipientID])
	h.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- n:
		case <-s.done:
		default:
			h.dropped.Add(context.Background(), 1)
			s.close(ErrSlowSubscriber)
		}
	}
}

// Subscribers returns how many local subscribers userID has.
func (h *Hub) Subscribers(userID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.byUser[userID])
}

// Notifications is the stream of the subscriber's notifications.
func (s *Subscription) Notifications() <-chan domain.Notification { return s.ch }

// Done is closed when the subscription ends; Err then says why.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// Err is nil after Close and ErrSlowSubscriber after a drop.
func (s *Subscription) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}

// Close ends the subscription. It is safe to call more than once.
func (s *Subscription) Close() { s.close(nil) }

func (s *Subscription) close(reason error) {
	s.once.Do(func() {
		s.err = reason
		close(s.done)
		s.hub.remove(s)
	})
}

func (h *Hub) remove(s *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.byUser[s.userID]
	i := slices.Index(subs, s)
	if i < 0 {
		return
	}
	subs = slices.Delete(subs, i, i+1)
	h.active.Add(context.Background(), -1)
	if len(subs) > 0 {
		h.byUser[s.userID] = subs
		return
	}
	delete(h.byUser, s.userID)
	if h.joined[s.userID] {
		delete(h.joined, s.userID)
		if err := h.fanout.Leave(context.Background(), s.userID); err != nil {
			h.logger.Warn("leave live fan-out failed", slog.String("user_id", s.userID), slog.String("error", err.Error()))
		}
	}
}

// CheckInvariants verifies that joined users are exactly those with local
// subscribers and that no user lists a subscriber twice.
func (h *Hub) CheckInvariants() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	for u, subs := range h.byUser {
		if len(subs) == 0 {
			return fmt.Errorf("hub: user %s kept with no subscribers", u)
		}
		if !h.joined[u] {
			return fmt.Errorf("hub: user %s has subscribers but is not joined", u)
		}
		for i, s := range subs {
			if s.userID != u || slices.Index(subs, s) != i {
				return fmt.Errorf("hub: user %s lists a wrong or repeated subscriber", u)
			}
		}
	}
	for u := range h.joined {
		if len(h.byUser[u]) == 0 {
			return fmt.Errorf("hub: user %s joined without subscribers", u)
		}
	}
	return nil
}
