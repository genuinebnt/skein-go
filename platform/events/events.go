package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Errors returned by this package. Test with errors.Is.
var (
	// ErrInvalidEnvelope means an envelope is missing a required field or
	// uses a reserved attribute.
	ErrInvalidEnvelope = errors.New("events: invalid envelope")
	// ErrUnknownTopic and ErrUnknownSubscription mean the name does not
	// exist (Memory) or the broker said NotFound (Pub/Sub).
	ErrUnknownTopic        = errors.New("events: unknown topic")
	ErrUnknownSubscription = errors.New("events: unknown subscription")
)

// Attribute names used on the wire.
const (
	AttrEventID     = "skein-event-id"
	AttrSource      = "skein-source"
	AttrTraceparent = "traceparent"
)

// Envelope is one event in transport form. See the package documentation.
type Envelope struct {
	ID          string
	Source      string
	OrderingKey string
	Traceparent string
	Attributes  map[string]string
	Payload     []byte
}

// NewEnvelope returns an envelope with a fresh UUIDv7 ID and the trace
// context of ctx.
func NewEnvelope(ctx context.Context, source, orderingKey string, payload []byte) (Envelope, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Envelope{}, fmt.Errorf("events: event id: %w", err)
	}
	e := Envelope{ID: id.String(), Source: source, OrderingKey: orderingKey, Payload: payload}
	InjectTrace(ctx, &e)
	return e, nil
}

// Validate reports whether the envelope can be published.
func (e Envelope) Validate() error {
	if e.ID == "" || e.Source == "" {
		return fmt.Errorf("%w: id and source are required", ErrInvalidEnvelope)
	}
	for k := range e.Attributes {
		if k == AttrEventID || k == AttrSource || k == AttrTraceparent || strings.HasPrefix(strings.ToLower(k), "goog") {
			return fmt.Errorf("%w: attribute %q is reserved", ErrInvalidEnvelope, k)
		}
	}
	return nil
}

// Clone returns a deep copy, so a stored envelope cannot be changed through
// the caller's slices or maps.
func (e Envelope) Clone() Envelope {
	e.Attributes = maps.Clone(e.Attributes)
	e.Payload = append([]byte(nil), e.Payload...)
	return e
}

var traceContext = propagation.TraceContext{}

// InjectTrace sets e.Traceparent from the span in ctx, unless it is
// already set (the outbox captures it when the event is enqueued, inside
// the request that caused it, and the relay must not overwrite it).
func InjectTrace(ctx context.Context, e *Envelope) {
	if e.Traceparent != "" {
		return
	}
	carrier := propagation.MapCarrier{}
	traceContext.Inject(ctx, carrier)
	e.Traceparent = carrier[AttrTraceparent]
}

// TraceContext returns ctx with the envelope's trace as the remote parent.
func (e Envelope) TraceContext(ctx context.Context) context.Context {
	if e.Traceparent == "" {
		return ctx
	}
	return traceContext.Extract(ctx, propagation.MapCarrier{AttrTraceparent: e.Traceparent})
}

// Message is an envelope as delivered to a subscriber.
type Message struct {
	Envelope
	PublishTime time.Time
	// DeliveryAttempt counts from 1. Pub/Sub reports it only on
	// subscriptions with a dead-letter policy; it is 0 otherwise.
	DeliveryAttempt int
}

// Publisher sends envelopes to a topic. Publish returns once the broker has
// accepted the message.
type Publisher interface {
	Publish(ctx context.Context, topic string, e Envelope) error
}

// Handler processes one message. Returning nil acks it; returning an
// error nacks it for redelivery.
type Handler func(ctx context.Context, msg Message) error

// Subscriber delivers a subscription's messages to a handler until ctx is
// cancelled, then returns nil (or the error that stopped it early).
type Subscriber interface {
	Subscribe(ctx context.Context, subscription string, h Handler) error
}

// toAttributes is the wire form of the envelope's metadata.
func toAttributes(e Envelope) map[string]string {
	attrs := make(map[string]string, len(e.Attributes)+3)
	maps.Copy(attrs, e.Attributes)
	attrs[AttrEventID] = e.ID
	attrs[AttrSource] = e.Source
	if e.Traceparent != "" {
		attrs[AttrTraceparent] = e.Traceparent
	}
	return attrs
}

// fromAttributes rebuilds an envelope from the wire form.
func fromAttributes(attrs map[string]string, orderingKey string, payload []byte) (Envelope, error) {
	e := Envelope{OrderingKey: orderingKey, Payload: payload, Attributes: map[string]string{}}
	for k, v := range attrs {
		switch k {
		case AttrEventID:
			e.ID = v
		case AttrSource:
			e.Source = v
		case AttrTraceparent:
			e.Traceparent = v
		default:
			e.Attributes[k] = v
		}
	}
	if e.ID == "" || e.Source == "" {
		return Envelope{}, fmt.Errorf("%w: message lacks %s or %s", ErrInvalidEnvelope, AttrEventID, AttrSource)
	}
	return e, nil
}

const instrumentation = "github.com/genuinebnt/skein-go/platform/events"

// dispatcher runs handlers with a consumer span and panic recovery; both
// implementations share it so they behave identically.
type dispatcher struct {
	tracer trace.Tracer
	logger *slog.Logger
	system string // messaging.system: "gcp_pubsub" or "memory"
}

func newDispatcher(tp trace.TracerProvider, logger *slog.Logger, system string) dispatcher {
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return dispatcher{tracer: tp.Tracer(instrumentation), logger: logger, system: system}
}

// dispatch runs h and reports whether to ack.
func (d dispatcher) dispatch(ctx context.Context, subscription string, h Handler, msg Message) (ack bool) {
	ctx, span := d.tracer.Start(msg.TraceContext(ctx), subscription+" process",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", d.system),
			attribute.String("messaging.operation.type", "process"),
			attribute.String("messaging.destination.subscription.name", subscription),
			attribute.String("messaging.message.id", msg.ID),
			attribute.Int("messaging.gcp_pubsub.message.delivery_attempt", msg.DeliveryAttempt),
		))
	defer span.End()
	defer func() {
		if p := recover(); p != nil {
			span.RecordError(fmt.Errorf("panic: %v", p))
			span.SetStatus(otelcodes.Error, "panic")
			d.logger.ErrorContext(ctx, "event handler panicked",
				slog.String("subscription", subscription), slog.String("event_id", msg.ID),
				slog.String("panic", fmt.Sprint(p)), slog.String("stack", string(debug.Stack())))
			ack = false
		}
	}()
	if err := h(ctx, msg); err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, "nack")
		d.logger.WarnContext(ctx, "event handler failed; nacking",
			slog.String("subscription", subscription), slog.String("event_id", msg.ID),
			slog.Int("delivery_attempt", msg.DeliveryAttempt), slog.String("error", err.Error()))
		return false
	}
	return true
}

// publishSpan starts a producer span for a publish.
func publishSpan(ctx context.Context, tracer trace.Tracer, system, topic string, e Envelope) (context.Context, trace.Span) {
	return tracer.Start(ctx, topic+" send",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", system),
			attribute.String("messaging.operation.type", "send"),
			attribute.String("messaging.destination.name", topic),
			attribute.String("messaging.message.id", e.ID),
		))
}

// sortedKeys returns m's keys in order, so wire encodings never depend on
// map order (ADR-0012).
func sortedKeys[V any](m map[string]V) []string {
	keys := slices.Collect(maps.Keys(m))
	slices.Sort(keys)
	return keys
}
