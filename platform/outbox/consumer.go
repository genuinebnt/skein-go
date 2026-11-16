package outbox

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/postgres"
)

const recordProcessed = `
INSERT INTO processed_events (consumer, event_id) VALUES ($1, $2)
ON CONFLICT (consumer, event_id) DO NOTHING`

// ProcessOnce records that consumer has processed eventID, inside tx, and
// reports whether this is the first time. Call it in the same transaction
// as the effect: if the effect rolls back, so does the record, and the
// redelivery is processed again.
func ProcessOnce(ctx context.Context, tx postgres.Execer, consumer, eventID string) (bool, error) {
	tag, err := tx.Exec(ctx, recordProcessed, consumer, eventID)
	if err != nil {
		return false, fmt.Errorf("outbox: record processed event: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Effect applies one event inside the consumer's transaction.
type Effect func(ctx context.Context, tx pgx.Tx, msg events.Message) error

// Consumer applies effects exactly once per event ID for one consumer name
// (the subscription, for example "search.documents-events").
type Consumer struct {
	db         postgres.Beginner
	name       string
	duplicates metric.Int64Counter
	attrs      metric.MeasurementOption
}

// NewConsumer returns a consumer named name on db. mp may be nil.
func NewConsumer(db postgres.Beginner, name string, mp metric.MeterProvider) (*Consumer, error) {
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	dup, err := mp.Meter("github.com/genuinebnt/skein-go/platform/outbox").Int64Counter("skein.events.duplicates",
		metric.WithDescription("Deliveries skipped because their event was already processed."))
	if err != nil {
		return nil, fmt.Errorf("outbox: metrics: %w", err)
	}
	return &Consumer{db: db, name: name, duplicates: dup, attrs: metric.WithAttributes(attribute.String("consumer", name))}, nil
}

// HandleOnce runs effect in a transaction unless msg was already processed.
func (c *Consumer) HandleOnce(ctx context.Context, msg events.Message, effect Effect) error {
	return postgres.WithTx(ctx, c.db, func(ctx context.Context, tx pgx.Tx) error {
		first, err := ProcessOnce(ctx, tx, c.name, msg.ID)
		if err != nil {
			return err
		}
		if !first {
			c.duplicates.Add(ctx, 1, c.attrs)
			return nil
		}
		return effect(ctx, tx, msg)
	})
}

// Handler adapts effect to an events.Handler with HandleOnce, so a
// subscription can be wired as sub.Subscribe(ctx, name, c.Handler(effect)).
func (c *Consumer) Handler(effect Effect) events.Handler {
	return func(ctx context.Context, msg events.Message) error {
		return c.HandleOnce(ctx, msg, effect)
	}
}
