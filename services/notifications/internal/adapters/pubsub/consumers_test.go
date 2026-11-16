package pubsub

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
	"github.com/genuinebnt/skein-go/services/notifications/internal/fakes"
)

func uid(n int) string { return fmt.Sprintf("01930000-0000-7000-8000-%012x", n) }

var (
	ada, grace, gus = uid(1), uid(2), uid(3)
	ws, note        = uid(100), uid(200)
	t0              = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
)

type recordingInvalidator struct{ users []string }

func (r *recordingInvalidator) InvalidateUser(_ context.Context, userID string) error {
	r.users = append(r.users, userID)
	return nil
}

type rig struct {
	t      *testing.T
	broker *events.Memory
	store  *fakes.Store
	inv    *recordingInvalidator
	reader *sdkmetric.ManualReader
	cons   *Consumers
	n      int
}

const topicRequests, topicDocuments, topicAgent, topicIdentity = "notifications.requests", "documents.events", "agent.events", "identity.events"

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, broker: events.NewMemory(events.MemoryOptions{}), store: fakes.NewStore(), inv: &recordingInvalidator{},
		reader: sdkmetric.NewManualReader()}
	for sub, topic := range map[string]string{SubscriptionRequests: topicRequests, SubscriptionDocuments: topicDocuments,
		SubscriptionAgent: topicAgent, SubscriptionIdentity: topicIdentity} {
		if err := r.broker.CreateSubscription(sub, topic, 3); err != nil {
			t.Fatal(err)
		}
	}
	authz := &fakes.Authorizer{Decide: func(user string, c app.Check, _ time.Time) bool {
		return user != gus || c.Action == app.ActionNoteRead
	}}
	dir := fakes.NewDirectory(
		fakes.DirectoryUser{Profile: app.Profile{UserID: ada, DisplayName: "Ada"}, Handle: "ada", Workspaces: []string{ws}},
		fakes.DirectoryUser{Profile: app.Profile{UserID: grace, DisplayName: "Grace"}, Handle: "grace", Workspaces: []string{ws}},
	)
	bus := &fakes.FanoutBus{}
	producer, err := app.NewProducer(app.ProducerConfig{Store: r.store, Authorizer: authz, Directory: dir,
		UnreadCache: fakes.NewUnreadCache(), Fanout: bus.Replica(), Clock: func() time.Time { return t0 }, NewID: fakes.IDs(1)})
	if err != nil {
		t.Fatal(err)
	}
	r.cons, err = NewConsumers(producer, Options{Invalidator: r.inv, MaxAttempts: 3,
		MeterProvider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(r.reader))})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rig) envelope(ev *skeinv1.Event, attrs map[string]string) events.Envelope {
	r.t.Helper()
	r.n++
	if ev.Id == "" {
		ev.Id = uid(5000 + r.n)
	}
	if ev.OccurredAt == nil {
		ev.OccurredAt = timestamppb.New(t0.Add(time.Duration(r.n) * time.Second))
	}
	payload, err := proto.Marshal(ev)
	if err != nil {
		r.t.Fatal(err)
	}
	return events.Envelope{ID: ev.Id, Source: "test", OrderingKey: note, Payload: payload, Attributes: attrs}
}

func (r *rig) publish(topic string, e events.Envelope) {
	r.t.Helper()
	if err := r.broker.Publish(context.Background(), topic, e); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) deliverAll() {
	r.t.Helper()
	for _, s := range Subscriptions {
		for range 5 {
			if _, err := r.broker.Deliver(context.Background(), s, r.cons.Handler(s)); err != nil {
				r.t.Fatal(err)
			}
		}
	}
	if err := r.broker.CheckInvariants(); err != nil {
		r.t.Fatal(err)
	}
	if err := r.store.CheckInvariants(); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) outcomes() map[string]int64 {
	r.t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.reader.Collect(context.Background(), &rm); err != nil {
		r.t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "skein.consumer.process.duration" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
				outcome, _ := dp.Attributes.Value("skein.consumer.outcome")
				typ, _ := dp.Attributes.Value("skein.event.type")
				out[typ.AsString()+"/"+outcome.AsString()] += int64(dp.Count)
			}
		}
	}
	return out
}

func actor(user string) *skeinv1.Subject { return &skeinv1.Subject{UserId: user} }

func kindsFor(store *fakes.Store, user string) []domain.Kind {
	var out []domain.Kind
	for _, n := range store.Notifications() {
		if n.RecipientID == user {
			out = append(out, n.Kind)
		}
	}
	slices.Sort(out)
	return out
}

func TestEventsOnEveryTopicReachTheInbox(t *testing.T) {
	r := newRig(t)
	r.publish(topicDocuments, r.envelope(&skeinv1.Event{Actor: actor(grace), Payload: &skeinv1.Event_NoteCreated{
		NoteCreated: &skeinv1.NoteCreated{NoteId: note, WorkspaceId: ws, Title: "Raft"}}}, nil))
	r.publish(topicDocuments, r.envelope(&skeinv1.Event{Actor: actor(grace), Payload: &skeinv1.Event_BlocksChanged{
		BlocksChanged: &skeinv1.BlocksChanged{NoteId: note, Blocks: []*skeinv1.ChangedBlock{{BlockId: "1:1", Kind: "paragraph", Text: "@ada look"}}}}}, nil))
	r.publish(topicRequests, r.envelope(&skeinv1.Event{Actor: actor(grace), Payload: &skeinv1.Event_NotificationRequested{
		NotificationRequested: &skeinv1.NotificationRequested{RecipientId: ada, Kind: skeinv1.NotificationKind_NOTIFICATION_KIND_COMMENT, Title: "hello",
			Location: &skeinv1.BlockRef{NoteId: note, BlockId: "1:1"}}}}, nil))
	r.publish(topicAgent, r.envelope(&skeinv1.Event{Actor: &skeinv1.Subject{UserId: ada, AgentRunId: "run-1"}, Payload: &skeinv1.Event_NotificationRequested{
		NotificationRequested: &skeinv1.NotificationRequested{RecipientId: ada, Title: "Approve deleting 3 notes?"}}},
		map[string]string{AttrAgentEvent: "confirmation_requested"}))
	r.publish(topicIdentity, r.envelope(&skeinv1.Event{Actor: actor(grace), Payload: &skeinv1.Event_AccessChanged{
		AccessChanged: &skeinv1.AccessChanged{WorkspaceId: ws, NoteId: note, UserId: gus}}}, nil))
	r.deliverAll()

	if got := kindsFor(r.store, ada); !slices.Equal(got, []domain.Kind{domain.KindMention, domain.KindComment, domain.KindAgentRunFinished}) {
		t.Errorf("ada: %v", got)
	}
	if got := kindsFor(r.store, gus); !slices.Equal(got, []domain.Kind{domain.KindShare}) {
		t.Errorf("gus: %v", got)
	}
	if !slices.Equal(r.inv.users, []string{gus}) {
		t.Errorf("authorization cache invalidated for %v", r.inv.users)
	}
	var confirmation domain.Notification
	for _, n := range r.store.Notifications() {
		if n.Kind == domain.KindAgentRunFinished {
			confirmation = n
		}
	}
	if confirmation.Source.Kind != domain.SourceAgentConfirmation || confirmation.Title != "Approve deleting 3 notes?" {
		t.Errorf("confirmation %+v", confirmation)
	}
	if got := r.outcomes(); got["blocks_changed/applied"] != 1 || got["access_changed/applied"] != 1 {
		t.Errorf("outcomes %v", got)
	}
}

func TestARepublishedEventIsAcknowledgedAsADuplicate(t *testing.T) {
	r := newRig(t)
	e := r.envelope(&skeinv1.Event{Actor: actor(grace), Payload: &skeinv1.Event_NotificationRequested{
		NotificationRequested: &skeinv1.NotificationRequested{RecipientId: ada, Kind: skeinv1.NotificationKind_NOTIFICATION_KIND_SHARE, Title: "x"}}}, nil)
	r.publish(topicRequests, e)
	r.publish(topicRequests, e)
	r.deliverAll()
	if n := len(r.store.Notifications()); n != 1 {
		t.Fatalf("%d notifications", n)
	}
	if got := r.outcomes(); got["notification_requested/applied"] != 1 || got["notification_requested/duplicate"] != 1 {
		t.Errorf("outcomes %v", got)
	}
}

func TestUnprocessableMessagesEndInTheDeadLetters(t *testing.T) {
	cases := map[string]struct {
		topic, sub string
		env        func(r *rig) events.Envelope
	}{
		"garbage payload": {topicRequests, SubscriptionRequests, func(r *rig) events.Envelope {
			return events.Envelope{ID: uid(9), Source: "test", Payload: []byte{0xff, 0xff, 0xff}}
		}},
		"agent notifying someone else": {topicAgent, SubscriptionAgent, func(r *rig) events.Envelope {
			return r.envelope(&skeinv1.Event{Actor: &skeinv1.Subject{UserId: ada, AgentRunId: "run"}, Payload: &skeinv1.Event_NotificationRequested{
				NotificationRequested: &skeinv1.NotificationRequested{RecipientId: grace, Title: "x"}}}, nil)
		}},
		"agent sending another kind": {topicAgent, SubscriptionAgent, func(r *rig) events.Envelope {
			return r.envelope(&skeinv1.Event{Actor: &skeinv1.Subject{UserId: ada, AgentRunId: "run"}, Payload: &skeinv1.Event_NotificationRequested{
				NotificationRequested: &skeinv1.NotificationRequested{RecipientId: ada, Kind: skeinv1.NotificationKind_NOTIFICATION_KIND_SHARE, Title: "x"}}}, nil)
		}},
		"request without a title": {topicRequests, SubscriptionRequests, func(r *rig) events.Envelope {
			return r.envelope(&skeinv1.Event{Payload: &skeinv1.Event_NotificationRequested{
				NotificationRequested: &skeinv1.NotificationRequested{RecipientId: ada, Kind: skeinv1.NotificationKind_NOTIFICATION_KIND_SHARE}}}, nil)
		}},
		"wrong payload on requests": {topicRequests, SubscriptionRequests, func(r *rig) events.Envelope {
			return r.envelope(&skeinv1.Event{Payload: &skeinv1.Event_NoteTrashed{NoteTrashed: &skeinv1.NoteTrashed{NoteId: note}}}, nil)
		}},
		"event id that is not a UUID": {topicDocuments, SubscriptionDocuments, func(r *rig) events.Envelope {
			return r.envelope(&skeinv1.Event{Id: "evt-1", Payload: &skeinv1.Event_NoteTrashed{NoteTrashed: &skeinv1.NoteTrashed{NoteId: note}}}, nil)
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.publish(c.topic, c.env(r))
			r.deliverAll()
			if dead := r.broker.DeadLetters(c.sub); len(dead) != 1 {
				t.Fatalf("%d dead letters", len(dead))
			}
			if n := len(r.store.Notifications()); n != 0 {
				t.Fatalf("%d notifications from an unprocessable message", n)
			}
			outcomes := r.outcomes()
			total := int64(0)
			for k, v := range outcomes {
				if k[len(k)-len("dead_lettered"):] != "dead_lettered" {
					t.Errorf("outcome %s counted for an unprocessable message", k)
				}
				total += v
			}
			if total != 3 {
				t.Errorf("outcomes %v, want 3 dead_lettered attempts", outcomes)
			}
		})
	}
}

func TestEventsOtherConsumersCareAboutAreAcknowledged(t *testing.T) {
	r := newRig(t)
	r.publish(topicDocuments, r.envelope(&skeinv1.Event{Payload: &skeinv1.Event_LinksChanged{LinksChanged: &skeinv1.LinksChanged{NoteId: note}}}, nil))
	r.deliverAll()
	if r.broker.Pending(SubscriptionDocuments) != 0 || len(r.broker.DeadLetters(SubscriptionDocuments)) != 0 {
		t.Fatal("LinksChanged was not acknowledged")
	}
}
