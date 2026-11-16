package outbox

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/postgres"
)

// SchemaSQL creates the outbox and processed_events tables.
//
//go:embed schema.sql
var SchemaSQL string

// ErrInvalidEvent means Enqueue was given an event it cannot store.
var ErrInvalidEvent = errors.New("outbox: invalid event")

const insertOutbox = `
INSERT INTO outbox (topic, event_id, source, ordering_key, traceparent, attributes, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7)`

// Enqueue stores an event for publication, inside the caller's transaction.
// The envelope's ID must be a UUID (events.NewEnvelope makes one), and its
// traceparent is taken from ctx unless already set.
func Enqueue(ctx context.Context, tx postgres.Execer, topic string, e events.Envelope) error {
	if topic == "" {
		return fmt.Errorf("%w: empty topic", ErrInvalidEvent)
	}
	if err := e.Validate(); err != nil {
		return errors.Join(ErrInvalidEvent, err)
	}
	if e.Payload == nil {
		e.Payload = []byte{}
	}
	events.InjectTrace(ctx, &e)
	attrs := e.Attributes
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrJSON, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("%w: attributes: %v", ErrInvalidEvent, err)
	}
	if _, err := tx.Exec(ctx, insertOutbox, topic, e.ID, e.Source, e.OrderingKey, e.Traceparent, attrJSON, e.Payload); err != nil {
		return fmt.Errorf("outbox: enqueue: %w", err)
	}
	return nil
}

// Record is a claimed outbox row.
type Record struct {
	ID        int64
	Topic     string
	Envelope  events.Envelope
	Attempts  int32
	CreatedAt time.Time
}

// Store claims due rows. PostgresStore is the real one; tests use a fake.
type Store interface {
	// Claim locks up to limit due rows, in id order, honouring the
	// per-key rule in the package documentation. The batch must end with
	// Commit or Rollback.
	Claim(ctx context.Context, limit int) (Batch, error)
}

// Batch is a set of claimed rows and the transaction that holds them.
type Batch interface {
	Records() []Record
	MarkPublished(ctx context.Context, ids []int64) error
	// MarkFailed increments the row's attempts and makes it due again
	// after retryAfter, measured on the store's clock.
	MarkFailed(ctx context.Context, id int64, retryAfter time.Duration, reason string) error
	Commit(ctx context.Context) error
	// Rollback releases the rows unchanged. Calling it after Commit is a
	// no-op.
	Rollback(ctx context.Context) error
}

// Pruner deletes published rows older than a retention period. The relay
// calls it periodically when the store implements it.
type Pruner interface {
	Prune(ctx context.Context, olderThan time.Duration) (int64, error)
}
