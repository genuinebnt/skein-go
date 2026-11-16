// Package redis implements two app ports on Redis: the cached unread count
// (UnreadCache) and the live fan-out across replicas (Fanout).
//
// # Unread counts
//
// The badge count is read on every ListInbox and changes far less often,
// so it is cached per user in a platform/redisx Cache ("notifications:c:
// unread:<user>") with a short TTL (60 s by default). The producer and
// MarkRead delete a user's entry after every commit that changes it;
// concurrent misses load once (singleflight). The cache fails open: if
// Redis is down, counts come from Postgres. Known limit (redisx): a load
// that started before an invalidation can store the old count after it;
// the TTL bounds how long that lasts.
//
// # Live fan-out
//
// A Subscribe stream lives on one replica, while the event that creates a
// notification is consumed by any replica. Each notification is published
// on the Redis channel "notifications:live:<user_id>". A replica subscribes
// to a user's channel when its hub gets that user's first local
// subscriber and unsubscribes after the last, so a replica only receives
// traffic for users connected to it. One Redis connection carries every
// subscription of a replica (go-redis resubscribes after a reconnect).
// Delivery is best effort, like Redis pub/sub itself: a message published
// while a replica is reconnecting is lost for its subscribers, who catch up
// through ListInbox.
//
// # Porting notes
//
// Rust: `redis-rs` or `fred` pub/sub with one connection and a Tokio task
// forwarding messages; the wire format is the JSON below.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

// Namespace prefixes every key and channel this service uses.
const Namespace = "notifications"

// UnreadCache is app.UnreadCache on a redisx store.
type UnreadCache struct {
	cache *redisx.Cache[uint32]
}

// NewUnreadCache returns the cache on store (a redisx.RedisStore in
// production, a redisx.MemoryStore in tests).
func NewUnreadCache(store redisx.Store, ttl time.Duration, mp metric.MeterProvider) (*UnreadCache, error) {
	if ttl == 0 {
		ttl = time.Minute
	}
	c, err := redisx.New(store, redisx.Options[uint32]{Name: "unread", TTL: ttl, MeterProvider: mp})
	if err != nil {
		return nil, err
	}
	return &UnreadCache{cache: c}, nil
}

// Get implements app.UnreadCache.
func (u *UnreadCache) Get(ctx context.Context, userID string, load func(context.Context) (uint32, error)) (uint32, error) {
	return u.cache.GetOrLoad(ctx, userID, func(ctx context.Context) (uint32, []string, error) {
		n, err := load(ctx)
		return n, nil, err
	})
}

// Invalidate implements app.UnreadCache.
func (u *UnreadCache) Invalidate(ctx context.Context, userIDs ...string) error {
	if len(userIDs) == 0 {
		return nil
	}
	return u.cache.Delete(ctx, userIDs...)
}

// liveMessage is the wire form of a pushed notification. Version lets the
// format change while replicas of two versions run side by side.
type liveMessage struct {
	Version     int       `json:"v"`
	ID          string    `json:"id"`
	RecipientID string    `json:"recipient_id"`
	Kind        uint8     `json:"kind"`
	Title       string    `json:"title"`
	Body        string    `json:"body,omitempty"`
	NoteID      string    `json:"note_id,omitempty"`
	BlockID     string    `json:"block_id,omitempty"`
	Start       uint32    `json:"start,omitempty"`
	End         uint32    `json:"end,omitempty"`
	ActorID     string    `json:"actor_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

const liveVersion = 1

func channel(userID string) string { return Namespace + ":live:" + userID }

// EncodeLive returns the wire form of n.
func EncodeLive(n domain.Notification) ([]byte, error) {
	return json.Marshal(liveMessage{Version: liveVersion, ID: n.ID, RecipientID: n.RecipientID, Kind: uint8(n.Kind),
		Title: n.Title, Body: n.Body, NoteID: n.Location.NoteID, BlockID: n.Location.BlockID, Start: n.Location.Start,
		End: n.Location.End, ActorID: n.ActorID, CreatedAt: n.CreatedAt})
}

// DecodeLive parses the wire form. Unknown versions are an error, so a
// newer replica's message is dropped rather than misread.
func DecodeLive(data []byte) (domain.Notification, error) {
	var m liveMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return domain.Notification{}, fmt.Errorf("redis: decode live message: %w", err)
	}
	if m.Version != liveVersion {
		return domain.Notification{}, fmt.Errorf("redis: live message version %d", m.Version)
	}
	return domain.Notification{ID: m.ID, RecipientID: m.RecipientID, Kind: domain.Kind(m.Kind), Title: m.Title, Body: m.Body,
		Location: domain.Location{NoteID: m.NoteID, BlockID: m.BlockID, Start: m.Start, End: m.End}, ActorID: m.ActorID,
		Occurrences: 1, InInbox: true, CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.CreatedAt.UTC()}, nil
}

// Fanout is app.Fanout on Redis pub/sub.
type Fanout struct {
	client goredis.UniversalClient
	pubsub *goredis.PubSub
	logger *slog.Logger

	mu      sync.Mutex
	deliver func(domain.Notification)
}

// NewFanout opens the replica's subscription connection. Call SetDeliver,
// then Run.
func NewFanout(ctx context.Context, client goredis.UniversalClient, logger *slog.Logger) *Fanout {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Fanout{client: client, pubsub: client.Subscribe(ctx), logger: logger}
}

// SetDeliver sets where received notifications go (the hub's Deliver).
func (f *Fanout) SetDeliver(deliver func(domain.Notification)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliver = deliver
}

// Publish implements app.Fanout.
func (f *Fanout) Publish(ctx context.Context, n domain.Notification) error {
	data, err := EncodeLive(n)
	if err != nil {
		return err
	}
	if err := f.client.Publish(ctx, channel(n.RecipientID), data).Err(); err != nil {
		return fmt.Errorf("redis: publish live notification: %w", err)
	}
	return nil
}

// Join implements app.Fanout.
func (f *Fanout) Join(ctx context.Context, userID string) error {
	if err := f.pubsub.Subscribe(ctx, channel(userID)); err != nil {
		return fmt.Errorf("redis: subscribe live channel: %w", err)
	}
	return nil
}

// Leave implements app.Fanout.
func (f *Fanout) Leave(ctx context.Context, userID string) error {
	if err := f.pubsub.Unsubscribe(ctx, channel(userID)); err != nil {
		return fmt.Errorf("redis: unsubscribe live channel: %w", err)
	}
	return nil
}

// Run forwards received messages to the deliver function until ctx ends,
// then closes the subscription connection.
func (f *Fanout) Run(ctx context.Context) error {
	ch := f.pubsub.Channel()
	defer func() { _ = f.pubsub.Close() }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return errors.New("redis: live subscription closed")
			}
			n, err := DecodeLive([]byte(msg.Payload))
			if err != nil {
				f.logger.Warn("dropped live message", slog.String("channel", msg.Channel), slog.String("error", err.Error()))
				continue
			}
			if !strings.HasSuffix(msg.Channel, ":"+n.RecipientID) {
				f.logger.Warn("dropped live message for another user", slog.String("channel", msg.Channel))
				continue
			}
			f.mu.Lock()
			deliver := f.deliver
			f.mu.Unlock()
			if deliver != nil {
				deliver(n)
			}
		}
	}
}
