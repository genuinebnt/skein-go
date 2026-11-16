package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"cloud.google.com/go/pubsub/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/genuinebnt/skein-go/platform/config"
)

// PubSubConfig configures NewPubSub. The client library reads
// PUBSUB_EMULATOR_HOST itself and, when it is set, talks plaintext to the
// emulator without credentials.
type PubSubConfig struct {
	Project string
	// MaxOutstandingMessages bounds unacked messages per subscriber, and
	// so memory and concurrency; default 100.
	MaxOutstandingMessages int
	Logger                 *slog.Logger
	TracerProvider         trace.TracerProvider
	// ClientOptions are passed to pubsub.NewClient (tests point it at
	// pstest).
	ClientOptions []option.ClientOption
}

// LoadPubSubConfig reads PUBSUB_PROJECT.
func LoadPubSubConfig(l *config.Loader) PubSubConfig {
	return PubSubConfig{Project: l.RequiredString("PUBSUB_PROJECT")}
}

// PubSub implements Publisher and Subscriber on Google Cloud Pub/Sub.
// Topics and subscriptions are created by infrastructure (Terraform, or
// deploy/compose/pubsub-init.sh locally), not by services.
type PubSub struct {
	client   *pubsub.Client
	cfg      PubSubConfig
	dispatch dispatcher
	tracer   trace.Tracer

	mu         sync.Mutex
	publishers map[string]*pubsub.Publisher
}

// NewPubSub connects to Pub/Sub.
func NewPubSub(ctx context.Context, cfg PubSubConfig) (*PubSub, error) {
	if cfg.Project == "" {
		return nil, errors.New("events: Pub/Sub project is required")
	}
	if cfg.MaxOutstandingMessages == 0 {
		cfg.MaxOutstandingMessages = 100
	}
	client, err := pubsub.NewClient(ctx, cfg.Project, cfg.ClientOptions...)
	if err != nil {
		return nil, fmt.Errorf("events: pubsub client: %w", err)
	}
	tp := cfg.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}
	return &PubSub{
		client:     client,
		cfg:        cfg,
		dispatch:   newDispatcher(tp, cfg.Logger, "gcp_pubsub"),
		tracer:     tp.Tracer(instrumentation),
		publishers: map[string]*pubsub.Publisher{},
	}, nil
}

func (p *PubSub) publisher(topic string) *pubsub.Publisher {
	p.mu.Lock()
	defer p.mu.Unlock()
	pub, ok := p.publishers[topic]
	if !ok {
		pub = p.client.Publisher(topic)
		pub.EnableMessageOrdering = true
		p.publishers[topic] = pub
	}
	return pub
}

// Publish implements Publisher. It waits for the broker's acknowledgement,
// which the outbox relay needs before it marks a row published.
func (p *PubSub) Publish(ctx context.Context, topic string, e Envelope) error {
	if err := e.Validate(); err != nil {
		return err
	}
	ctx, span := publishSpan(ctx, p.tracer, "gcp_pubsub", topic, e)
	defer span.End()
	InjectTrace(ctx, &e)

	pub := p.publisher(topic)
	result := pub.Publish(ctx, &pubsub.Message{
		Data: e.Payload, Attributes: toAttributes(e), OrderingKey: e.OrderingKey,
	})
	if _, err := result.Get(ctx); err != nil {
		// After a failure the client pauses the key so later messages
		// cannot overtake this one. The caller retries this message, so
		// resume the key for that retry.
		if e.OrderingKey != "" {
			pub.ResumePublish(e.OrderingKey)
		}
		span.RecordError(err)
		if status.Code(err) == codes.NotFound {
			return fmt.Errorf("%w: %q: %v", ErrUnknownTopic, topic, err)
		}
		return fmt.Errorf("events: publish to %s: %w", topic, err)
	}
	return nil
}

// Subscribe implements Subscriber.
func (p *PubSub) Subscribe(ctx context.Context, subscription string, h Handler) error {
	sub := p.client.Subscriber(subscription)
	sub.ReceiveSettings.MaxOutstandingMessages = p.cfg.MaxOutstandingMessages
	err := sub.Receive(ctx, func(ctx context.Context, m *pubsub.Message) {
		env, err := fromAttributes(m.Attributes, m.OrderingKey, m.Data)
		if err != nil {
			p.dispatch.logger.ErrorContext(ctx, "malformed event; nacking toward the dead-letter topic",
				slog.String("subscription", subscription), slog.String("message_id", m.ID), slog.String("error", err.Error()))
			m.Nack()
			return
		}
		msg := Message{Envelope: env, PublishTime: m.PublishTime}
		if m.DeliveryAttempt != nil {
			msg.DeliveryAttempt = *m.DeliveryAttempt
		}
		if p.dispatch.dispatch(ctx, subscription, h, msg) {
			m.Ack()
		} else {
			m.Nack()
		}
	})
	if status.Code(err) == codes.NotFound {
		return fmt.Errorf("%w: %q: %v", ErrUnknownSubscription, subscription, err)
	}
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("events: receive %s: %w", subscription, err)
	}
	return nil
}

// Close flushes pending publishes and closes the client.
func (p *PubSub) Close() error {
	p.mu.Lock()
	for _, pub := range p.publishers {
		pub.Stop()
	}
	p.publishers = map[string]*pubsub.Publisher{}
	p.mu.Unlock()
	return p.client.Close()
}
