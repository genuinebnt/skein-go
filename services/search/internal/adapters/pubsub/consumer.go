// Package pubsub runs the search service's subscriptions
// (search.documents-events, search.identity-events): it continues each
// event's trace, hands the message to the Projector, and records the
// consumer metrics of docs/observability.md.
package pubsub

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
)

// Handle applies one message and says what it did.
type Handle func(ctx context.Context, msg events.Message) (app.Outcome, error)

// Config configures a Consumer.
type Config struct {
	// Subscription is the Pub/Sub subscription name.
	Subscription string
	// MaxAttempts is the subscription's dead-letter threshold
	// (deploy/compose/pubsub-init.sh: 10). A failure on that attempt is
	// reported as dead_lettered.
	MaxAttempts    int
	Logger         *slog.Logger
	MeterProvider  metric.MeterProvider
	TracerProvider trace.TracerProvider
}

// Consumer delivers one subscription's messages to a Handle.
type Consumer struct {
	cfg      Config
	sub      events.Subscriber
	handle   Handle
	tracer   trace.Tracer
	duration metric.Float64Histogram
	age      metric.Float64Histogram
}

// Boundaries from docs/observability.md, "Histogram buckets".
var (
	durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}
	ageBuckets      = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300}
)

// NewConsumer returns a consumer of cfg.Subscription on sub.
func NewConsumer(sub events.Subscriber, handle Handle, cfg Config) (*Consumer, error) {
	if cfg.Logger == nil {
		cfg.Logger = logging.Discard()
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 10
	}
	if cfg.MeterProvider == nil {
		cfg.MeterProvider = otel.GetMeterProvider()
	}
	if cfg.TracerProvider == nil {
		cfg.TracerProvider = otel.GetTracerProvider()
	}
	meter := cfg.MeterProvider.Meter("github.com/genuinebnt/skein-go/services/search")
	duration, err := meter.Float64Histogram("skein.consumer.process.duration", metric.WithUnit("s"),
		metric.WithDescription("Time to process one event."), metric.WithExplicitBucketBoundaries(durationBuckets...))
	if err != nil {
		return nil, err
	}
	age, err := meter.Float64Histogram("skein.consumer.event.age", metric.WithUnit("s"),
		metric.WithDescription("Time from an event's occurrence to the start of its processing."), metric.WithExplicitBucketBoundaries(ageBuckets...))
	if err != nil {
		return nil, err
	}
	return &Consumer{cfg: cfg, sub: sub, handle: handle, tracer: cfg.TracerProvider.Tracer("github.com/genuinebnt/skein-go/services/search"),
		duration: duration, age: age}, nil
}

// Run consumes until ctx ends.
func (c *Consumer) Run(ctx context.Context) error {
	return c.sub.Subscribe(ctx, c.cfg.Subscription, c.Process)
}

// Process handles one message: a span continuing the publisher's trace,
// the projector, metrics and one log line. Returning an error nacks it.
func (c *Consumer) Process(ctx context.Context, msg events.Message) error {
	start := time.Now()
	ctx = msg.TraceContext(ctx)
	ctx, span := c.tracer.Start(ctx, c.cfg.Subscription+" process", trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attribute.String("messaging.system", "gcp_pubsub"), attribute.String("messaging.message.id", msg.ID)))
	defer span.End()

	eventType := "unknown"
	if e, err := app.DecodeEvent(msg.Payload); err == nil {
		eventType = app.EventType(e)
		if t := e.GetOccurredAt(); t != nil {
			c.age.Record(ctx, start.Sub(t.AsTime()).Seconds(), metric.WithAttributes(
				attribute.String("messaging.system", "gcp_pubsub"),
				attribute.String("messaging.destination.subscription.name", c.cfg.Subscription)))
		}
	}
	outcome, err := c.handle(ctx, msg)
	errorType := ""
	if err != nil {
		outcome, errorType = "retry", "handler_error"
		if errors.Is(err, app.ErrUnprocessable) {
			errorType = "unprocessable"
		}
		if errors.Is(err, app.ErrUnprocessable) || msg.DeliveryAttempt >= c.cfg.MaxAttempts {
			outcome = "dead_lettered"
		}
		span.RecordError(err)
	}
	c.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
		attribute.String("messaging.system", "gcp_pubsub"),
		attribute.String("messaging.destination.subscription.name", c.cfg.Subscription),
		attribute.String("skein.event.type", eventType),
		attribute.String("skein.consumer.outcome", string(outcome)),
		attribute.String("error.type", errorType)))
	log := c.cfg.Logger.With("event_id", msg.ID, "event_type", eventType, "outcome", string(outcome), "subscription", c.cfg.Subscription)
	if err != nil {
		log.ErrorContext(ctx, "event not applied", "error", err, "delivery_attempt", msg.DeliveryAttempt)
		return err
	}
	log.DebugContext(ctx, "event processed", "duration_s", time.Since(start).Seconds())
	return nil
}
