// Package pubsub consumes documents.events on the identity.documents-events
// subscription and keeps identity's note projection current through
// app.ApplyNoteEvent, which records each event ID in the same transaction
// as its effect (idempotent consumer, ADR-0006).
//
// Outcomes follow docs/observability.md: applied, duplicate, retry (the
// handler failed and the message is nacked) and dead_lettered (the last
// allowed attempt failed, or the event is malformed). A malformed event is
// nacked like any failure, because platform/events has no way to send a
// message to the dead-letter topic directly; Pub/Sub moves it after
// MaxDeliveryAttempts. Payloads identity does not project (blocks, links,
// renames) are acknowledged at once and counted as applied with no effect.
package pubsub

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/logging"

	identityevents "github.com/genuinebnt/skein-go/services/identity/internal/adapters/events"
	"github.com/genuinebnt/skein-go/services/identity/internal/app"
)

// MaxDeliveryAttempts matches the dead-letter policy in
// deploy/compose/pubsub-init.sh.
const MaxDeliveryAttempts = 10

// NoteEventApplier is the use case the consumer drives.
type NoteEventApplier interface {
	ApplyNoteEvent(ctx context.Context, consumer, eventID string, ev app.NoteEvent) (app.ConsumeOutcome, error)
}

// Consumer handles documents events.
type Consumer struct {
	app      NoteEventApplier
	name     string
	log      *slog.Logger
	duration metric.Float64Histogram
	age      metric.Float64Histogram
}

// NewConsumer returns a consumer for subscription name. mp may be nil.
func NewConsumer(a NoteEventApplier, name string, log *slog.Logger, mp metric.MeterProvider) (*Consumer, error) {
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	if log == nil {
		log = logging.Discard()
	}
	meter := mp.Meter("github.com/genuinebnt/skein-go/services/identity/internal/adapters/pubsub")
	duration, err := meter.Float64Histogram("skein.consumer.process.duration", metric.WithUnit("s"),
		metric.WithDescription("Time to process one delivered event."),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10))
	if err != nil {
		return nil, err
	}
	age, err := meter.Float64Histogram("skein.consumer.event.age", metric.WithUnit("s"),
		metric.WithDescription("Time from an event's occurrence to the start of its processing."),
		metric.WithExplicitBucketBoundaries(0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300))
	if err != nil {
		return nil, err
	}
	return &Consumer{app: a, name: name, log: log, duration: duration, age: age}, nil
}

// Handle implements events.Handler.
func (c *Consumer) Handle(ctx context.Context, msg events.Message) error {
	start := time.Now()
	ctx = msg.TraceContext(ctx)
	ev, kind, err := identityevents.DecodeNoteEvent(msg)
	base := []attribute.KeyValue{
		attribute.String("messaging.system", "gcp_pubsub"),
		attribute.String("messaging.destination.subscription.name", c.name),
	}
	outcome := string(app.OutcomeApplied)
	switch {
	case errors.Is(err, identityevents.ErrIrrelevant):
		err = nil
	case err == nil:
		c.age.Record(ctx, time.Since(ev.OccurredAt).Seconds(), metric.WithAttributes(base...))
		var o app.ConsumeOutcome
		o, err = c.app.ApplyNoteEvent(ctx, c.name, msg.ID, ev)
		outcome = string(o)
	}
	attrs := append(base, attribute.String("skein.event.type", kind))
	if err != nil {
		outcome = "retry"
		if msg.DeliveryAttempt >= MaxDeliveryAttempts || errors.Is(err, identityevents.ErrMalformed) {
			outcome = "dead_lettered"
		}
		errType := "processing"
		if errors.Is(err, identityevents.ErrMalformed) {
			errType = "malformed"
		}
		attrs = append(attrs, attribute.String("error.type", errType))
		c.log.ErrorContext(ctx, "documents event not applied", "event_id", msg.ID, "subscription", c.name,
			"delivery_attempt", msg.DeliveryAttempt, "outcome", outcome, "error", err)
	}
	attrs = append(attrs, attribute.String("skein.consumer.outcome", outcome))
	c.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
	return err
}
