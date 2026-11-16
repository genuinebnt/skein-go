// Package pubsub wires the notification service's four subscriptions to
// the producer: it decodes each envelope's skein.v1.Event, turns the
// payload into an app call, and reports the outcome in the consumer
// metrics of docs/observability.md.
//
// # What triggers what
//
//	subscription                     payload                 effect
//	notifications.requests           NotificationRequested   Producer.Request: store and deliver as asked
//	notifications.agent-events       NotificationRequested   Producer.AgentEvent: AGENT_RUN_FINISHED to the run's
//	                                                         user; attribute skein-agent-event=confirmation_requested
//	                                                         marks a confirmation request
//	notifications.documents-events   NoteCreated/Renamed/    note projection (titles, creator, workspace)
//	                                 Trashed/Restored
//	                                 NotePurged              erase the note's notifications and mentions
//	                                 BlocksChanged           MENTION for users newly @mentioned in a block
//	                                 LinksChanged and others acknowledged, no effect
//	notifications.identity-events    AccessChanged           SHARE / ACCESS_CHANGED, and drop the user's cached
//	                                                         authorization answers first
//
// Comments have no event payload yet (events.proto); the service README
// proposes CommentAdded, and the seed loader drives Producer.Comment
// directly until it exists.
//
// # Failures
//
// Idempotency is the producer's (one processed-event row per consumer and
// event ID, in the effect's transaction), so this layer only decides ack
// or nack. A handler error nacks; Pub/Sub redelivers with backoff and, after
// the subscription's maximum attempts (10, deploy/compose/pubsub-init.sh),
// moves the message to the topic's dead-letter topic. A message that can
// never succeed (undecodable, invalid, forbidden) is nacked too, so it
// reaches the dead letters for a person to inspect rather than vanishing;
// it is counted as dead_lettered from its first attempt.
//
// # Porting notes
//
// Rust: prost-generated Event with a match on the payload oneof; the same
// routing table.
package pubsub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

// Subscriptions this service consumes (deploy/compose/pubsub-init.sh).
const (
	SubscriptionRequests  = "notifications.requests"
	SubscriptionDocuments = "notifications.documents-events"
	SubscriptionAgent     = "notifications.agent-events"
	SubscriptionIdentity  = "notifications.identity-events"
)

// Subscriptions lists them in the order main starts them.
var Subscriptions = []string{SubscriptionRequests, SubscriptionDocuments, SubscriptionAgent, SubscriptionIdentity}

// AttrAgentEvent is the envelope attribute the agent sets on a
// confirmation request ("confirmation_requested"); absent or
// "run_finished" means the run finished.
const AttrAgentEvent = "skein-agent-event"

// ErrUnprocessable marks a message that can never succeed.
var ErrUnprocessable = errors.New("unprocessable event")

// AuthorizationInvalidator drops cached authorization answers for a user;
// the identity adapter's cache implements it.
type AuthorizationInvalidator interface {
	InvalidateUser(ctx context.Context, userID string) error
}

// Consumers routes messages to the producer.
type Consumers struct {
	producer    *app.Producer
	invalidator AuthorizationInvalidator
	logger      *slog.Logger
	maxAttempts int

	duration metric.Float64Histogram
	age      metric.Float64Histogram
}

// Options configure NewConsumers.
type Options struct {
	// Invalidator may be nil when nothing caches authorization.
	Invalidator AuthorizationInvalidator
	// MaxAttempts is the subscriptions' maximum delivery attempts; 10.
	MaxAttempts   int
	Logger        *slog.Logger
	MeterProvider metric.MeterProvider
}

// NewConsumers returns the router.
func NewConsumers(producer *app.Producer, opts Options) (*Consumers, error) {
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = events.DefaultMaxDeliveryAttempts
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.MeterProvider == nil {
		opts.MeterProvider = otel.GetMeterProvider()
	}
	meter := opts.MeterProvider.Meter("github.com/genuinebnt/skein-go/services/notifications/internal/adapters/pubsub")
	duration, err1 := meter.Float64Histogram("skein.consumer.process.duration", metric.WithUnit("s"),
		metric.WithDescription("Time to process one delivered event."),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10))
	age, err2 := meter.Float64Histogram("skein.consumer.event.age", metric.WithUnit("s"),
		metric.WithDescription("Time from Event.occurred_at to the start of processing."),
		metric.WithExplicitBucketBoundaries(0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300))
	if err := errors.Join(err1, err2); err != nil {
		return nil, fmt.Errorf("pubsub: metrics: %w", err)
	}
	return &Consumers{producer: producer, invalidator: opts.Invalidator, logger: opts.Logger, maxAttempts: opts.MaxAttempts,
		duration: duration, age: age}, nil
}

// Handler returns the events.Handler for one subscription.
func (c *Consumers) Handler(subscription string) events.Handler {
	return func(ctx context.Context, msg events.Message) error {
		_, err := c.Process(ctx, subscription, msg)
		return err
	}
}

// Process handles one delivered message and returns its outcome as the
// skein.consumer.outcome metric reports it: applied, duplicate, retry or
// dead_lettered. A non-nil error means nack.
func (c *Consumers) Process(ctx context.Context, subscription string, msg events.Message) (string, error) {
	start := time.Now()
	eventType, outcome, err := c.dispatch(ctx, subscription, msg)
	attrs := []attribute.KeyValue{
		attribute.String("messaging.system", "gcp_pubsub"),
		attribute.String("messaging.destination.subscription.name", subscription),
		attribute.String("skein.event.type", eventType),
	}
	if err != nil {
		permanent := isPermanent(err)
		outcome = "retry"
		if permanent || msg.DeliveryAttempt >= c.maxAttempts {
			outcome = "dead_lettered"
		}
		errType := "transient"
		if permanent {
			errType = "unprocessable"
		}
		attrs = append(attrs, attribute.String("error.type", errType))
		level := slog.LevelWarn
		if outcome == "dead_lettered" {
			level = slog.LevelError
		}
		c.logger.Log(ctx, level, "event not processed", slog.String("subscription", subscription),
			slog.String("event_id", msg.ID), slog.String("event_type", eventType), slog.Int("delivery_attempt", msg.DeliveryAttempt),
			slog.String("outcome", outcome), slog.String("error", err.Error()))
	}
	attrs = append(attrs, attribute.String("skein.consumer.outcome", outcome))
	c.duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
	return outcome, err
}

func isPermanent(err error) bool {
	return errors.Is(err, ErrUnprocessable) || errors.Is(err, domain.ErrInvalidArgument) || errors.Is(err, app.ErrForbidden)
}

// Decode parses an envelope payload as a skein.v1.Event.
func Decode(payload []byte) (*skeinv1.Event, error) {
	var ev skeinv1.Event
	if err := proto.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("%w: decode event: %v", ErrUnprocessable, err)
	}
	return &ev, nil
}

// dispatch decodes and routes one message, returning the payload type for
// metrics and the producer's outcome.
func (c *Consumers) dispatch(ctx context.Context, subscription string, msg events.Message) (string, string, error) {
	ev, err := Decode(msg.Payload)
	if err != nil {
		return "undecodable", "", err
	}
	meta := app.Event{Consumer: subscription, ID: ev.GetId(), OccurredAt: msg.PublishTime}
	if meta.ID == "" {
		meta.ID = msg.ID
	}
	if ev.GetOccurredAt() != nil {
		meta.OccurredAt = ev.GetOccurredAt().AsTime()
		c.age.Record(ctx, time.Since(meta.OccurredAt).Seconds(), metric.WithAttributes(
			attribute.String("messaging.system", "gcp_pubsub"),
			attribute.String("messaging.destination.subscription.name", subscription)))
	}
	if meta.OccurredAt.IsZero() {
		meta.OccurredAt = time.Now()
	}
	if a := ev.GetActor(); a != nil {
		meta.ActorUserID, meta.ActorAgentRunID = a.GetUserId(), a.GetAgentRunId()
	}
	ctx = logging.WithSubject(ctx, meta.ActorUserID, meta.ActorAgentRunID)

	eventType := payloadType(ev)
	var outcome app.Outcome
	switch subscription {
	case SubscriptionRequests:
		outcome, err = c.request(ctx, meta, ev)
	case SubscriptionAgent:
		outcome, err = c.agent(ctx, meta, ev, msg.Attributes[AttrAgentEvent])
	case SubscriptionDocuments:
		outcome, err = c.documents(ctx, meta, ev)
	case SubscriptionIdentity:
		outcome, err = c.identity(ctx, meta, ev)
	default:
		err = fmt.Errorf("%w: unknown subscription %q", ErrUnprocessable, subscription)
	}
	return eventType, string(outcome), err
}

// payloadType is the snake_case payload name used as skein.event.type.
func payloadType(ev *skeinv1.Event) string {
	switch ev.GetPayload().(type) {
	case *skeinv1.Event_NoteCreated:
		return "note_created"
	case *skeinv1.Event_NoteRenamed:
		return "note_renamed"
	case *skeinv1.Event_NoteTrashed:
		return "note_trashed"
	case *skeinv1.Event_NoteRestored:
		return "note_restored"
	case *skeinv1.Event_NotePurged:
		return "note_purged"
	case *skeinv1.Event_BlocksChanged:
		return "blocks_changed"
	case *skeinv1.Event_LinksChanged:
		return "links_changed"
	case *skeinv1.Event_AccessChanged:
		return "access_changed"
	case *skeinv1.Event_NotificationRequested:
		return "notification_requested"
	default:
		return "unknown"
	}
}

// Location converts a proto BlockRef.
func Location(ref *skeinv1.BlockRef) domain.Location {
	if ref == nil {
		return domain.Location{}
	}
	return domain.Location{NoteID: ref.GetNoteId(), BlockID: ref.GetBlockId(), Start: ref.GetStart(), End: ref.GetEnd()}
}

func (c *Consumers) request(ctx context.Context, meta app.Event, ev *skeinv1.Event) (app.Outcome, error) {
	nr := ev.GetNotificationRequested()
	if nr == nil {
		return "", fmt.Errorf("%w: %s on %s", ErrUnprocessable, payloadType(ev), SubscriptionRequests)
	}
	return c.producer.Request(ctx, meta, domain.Request{RecipientID: nr.GetRecipientId(), Kind: domain.Kind(nr.GetKind()),
		Title: nr.GetTitle(), Body: nr.GetBody(), Location: Location(nr.GetLocation())})
}

func (c *Consumers) agent(ctx context.Context, meta app.Event, ev *skeinv1.Event, agentEvent string) (app.Outcome, error) {
	nr := ev.GetNotificationRequested()
	if nr == nil {
		// The agent may publish other events here later; they concern
		// other consumers.
		return app.OutcomeApplied, nil
	}
	if k := nr.GetKind(); k != skeinv1.NotificationKind_NOTIFICATION_KIND_UNSPECIFIED && k != skeinv1.NotificationKind_NOTIFICATION_KIND_AGENT_RUN_FINISHED {
		return "", fmt.Errorf("%w: agent may only send AGENT_RUN_FINISHED, got %s", ErrUnprocessable, k)
	}
	var confirmation bool
	switch agentEvent {
	case "", "run_finished":
	case "confirmation_requested":
		confirmation = true
	default:
		return "", fmt.Errorf("%w: %s=%q", ErrUnprocessable, AttrAgentEvent, agentEvent)
	}
	return c.producer.AgentEvent(ctx, meta, app.AgentEvent{RecipientID: nr.GetRecipientId(), Title: nr.GetTitle(), Body: nr.GetBody(),
		Location: Location(nr.GetLocation()), Confirmation: confirmation})
}

func (c *Consumers) documents(ctx context.Context, meta app.Event, ev *skeinv1.Event) (app.Outcome, error) {
	switch p := ev.GetPayload().(type) {
	case *skeinv1.Event_NoteCreated:
		return c.producer.NoteCreated(ctx, meta, p.NoteCreated.GetNoteId(), p.NoteCreated.GetWorkspaceId(), p.NoteCreated.GetTitle())
	case *skeinv1.Event_NoteRenamed:
		return c.producer.NoteRenamed(ctx, meta, p.NoteRenamed.GetNoteId(), p.NoteRenamed.GetTitle())
	case *skeinv1.Event_NoteTrashed:
		return c.producer.NoteTrashed(ctx, meta, p.NoteTrashed.GetNoteId())
	case *skeinv1.Event_NoteRestored:
		return c.producer.NoteRestored(ctx, meta, p.NoteRestored.GetNoteId())
	case *skeinv1.Event_NotePurged:
		return c.producer.NotePurged(ctx, meta, p.NotePurged.GetNoteId())
	case *skeinv1.Event_BlocksChanged:
		bc := p.BlocksChanged
		in := app.BlocksChangedEvent{NoteID: bc.GetNoteId(), RemovedBlockIDs: bc.GetRemovedBlockIds()}
		for _, b := range bc.GetBlocks() {
			in.Blocks = append(in.Blocks, app.ChangedBlock{ID: b.GetBlockId(), Kind: b.GetKind(), Text: b.GetText()})
		}
		return c.producer.BlocksChanged(ctx, meta, in)
	default:
		return app.OutcomeApplied, nil
	}
}

func (c *Consumers) identity(ctx context.Context, meta app.Event, ev *skeinv1.Event) (app.Outcome, error) {
	ac := ev.GetAccessChanged()
	if ac == nil {
		return app.OutcomeApplied, nil
	}
	if c.invalidator != nil && ac.GetUserId() != "" {
		// Before the producer asks identity about this user, so its answer
		// reflects the change.
		if err := c.invalidator.InvalidateUser(ctx, ac.GetUserId()); err != nil {
			return "", fmt.Errorf("pubsub: invalidate authorization cache: %w", err)
		}
	}
	return c.producer.AccessChanged(ctx, meta, app.AccessChangedEvent{WorkspaceID: ac.GetWorkspaceId(), NoteID: ac.GetNoteId(),
		UserID: ac.GetUserId(), Revoked: ac.GetRevoked()})
}

// Run subscribes every subscription on sub until ctx ends. If one
// subscription stops with an error, the others are stopped too and the
// error is returned, so the process exits instead of running deaf.
func (c *Consumers) Run(ctx context.Context, sub events.Subscriber) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(Subscriptions))
	for _, s := range Subscriptions {
		go func() {
			err := sub.Subscribe(ctx, s, c.Handler(s))
			if err != nil {
				err = fmt.Errorf("pubsub: %s: %w", s, err)
				cancel()
			}
			errs <- err
		}()
	}
	var first error
	for range Subscriptions {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	return first
}
