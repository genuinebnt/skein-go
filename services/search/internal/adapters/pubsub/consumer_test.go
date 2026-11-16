package pubsub_test

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/services/search/internal/adapters/pubsub"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/internal/fakes"
)

func outcomes(t *testing.T, reader *sdkmetric.ManualReader) map[string]uint64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]uint64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "skein.consumer.process.duration" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
				v, _ := dp.Attributes.Value("skein.consumer.outcome")
				out[v.AsString()] += dp.Count
			}
		}
	}
	return out
}

func TestConsumerAppliesRedeliveriesOnceAndDeadLettersPoison(t *testing.T) {
	bus := events.NewMemory(events.MemoryOptions{})
	bus.CreateTopic("documents.events")
	if err := bus.CreateSubscription(app.DocumentsConsumer, "documents.events", 3); err != nil {
		t.Fatal(err)
	}
	store := fakes.NewStore()
	reader := sdkmetric.NewManualReader()
	consumer, err := pubsub.NewConsumer(bus, app.NewProjector(store, nil, nil, nil).HandleDocuments,
		pubsub.Config{Subscription: app.DocumentsConsumer, MaxAttempts: 3, MeterProvider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := proto.Marshal(&skeinv1.Event{Payload: &skeinv1.Event_NoteCreated{NoteCreated: &skeinv1.NoteCreated{NoteId: "n", WorkspaceId: "w", Title: "T"}}})
	good := events.Envelope{ID: "0193a000-0000-7000-8000-000000000001", Source: "documents", OrderingKey: "n", Payload: payload}
	ctx := context.Background()
	for range 2 { // the relay's crash-and-retry duplicate
		if err := bus.Publish(ctx, "documents.events", good); err != nil {
			t.Fatal(err)
		}
	}
	if err := bus.Publish(ctx, "documents.events", events.Envelope{ID: "0193a000-0000-7000-8000-000000000002", Source: "documents", OrderingKey: "p", Payload: []byte{0xff}}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := bus.Deliver(ctx, app.DocumentsConsumer, consumer.Process); err != nil {
			t.Fatal(err)
		}
	}
	changes, _ := store.Changes(ctx, 0, 10)
	if len(changes) != 1 {
		t.Fatalf("%d changes for one event delivered twice", len(changes))
	}
	if len(bus.DeadLetters(app.DocumentsConsumer)) != 1 {
		t.Fatal("poison message not dead-lettered")
	}
	got := outcomes(t, reader)
	if got["applied"] != 1 || got["duplicate"] != 1 || got["dead_lettered"] != 3 {
		t.Fatalf("outcomes %v", got)
	}
}
