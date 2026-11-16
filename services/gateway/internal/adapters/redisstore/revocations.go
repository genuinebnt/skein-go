package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// RevocationBus implements app.RevocationBus with Redis pub/sub.
type RevocationBus struct {
	client  *redis.Client
	channel string
	log     *slog.Logger
}

// NewRevocationBus returns a bus on "<namespace>:access-revoked".
func NewRevocationBus(client *redis.Client, namespace string, log *slog.Logger) *RevocationBus {
	return &RevocationBus{client: client, channel: namespace + ":access-revoked", log: log}
}

// Publish implements app.RevocationBus.
func (b *RevocationBus) Publish(ctx context.Context, r domain.Revocation) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := b.client.Publish(ctx, b.channel, data).Err(); err != nil {
		return fmt.Errorf("redisstore: publish revocation: %w", err)
	}
	return nil
}

// Subscribe implements app.RevocationBus. go-redis resubscribes by itself
// after a lost connection; messages published meanwhile are lost (see the
// package documentation).
func (b *RevocationBus) Subscribe(ctx context.Context, handle func(domain.Revocation)) error {
	sub := b.client.Subscribe(ctx, b.channel)
	defer func() { _ = sub.Close() }()
	// Receive waits for the subscription confirmation, so a publication
	// made after Subscribe has started is not missed by a racing caller.
	if _, err := sub.Receive(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("redisstore: subscribe: %w", err)
	}
	messages := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-messages:
			if !ok {
				return nil
			}
			var r domain.Revocation
			if err := json.Unmarshal([]byte(msg.Payload), &r); err != nil || r.UserID == "" {
				b.log.WarnContext(ctx, "ignoring malformed revocation broadcast")
				continue
			}
			handle(r)
		}
	}
}
