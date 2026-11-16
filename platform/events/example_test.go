package events_test

import (
	"context"
	"fmt"

	"github.com/genuinebnt/skein-go/platform/events"
)

func Example() {
	ctx := context.Background()
	// In a service: events.NewPubSub(ctx, events.LoadPubSubConfig(loader)).
	bus := events.NewMemory(events.MemoryOptions{})
	_ = bus.CreateSubscription("search.documents-events", "documents.events", 0)

	var publisher events.Publisher = bus
	env, _ := events.NewEnvelope(ctx, "documents", "note-0193", []byte("<encoded skein.v1.Event>"))
	_ = publisher.Publish(ctx, "documents.events", env)

	handle := func(ctx context.Context, msg events.Message) error {
		fmt.Printf("from %s, key %s, attempt %d\n", msg.Source, msg.OrderingKey, msg.DeliveryAttempt)
		return nil // ack
	}
	acked, _ := bus.Deliver(ctx, "search.documents-events", handle)
	fmt.Println("acked", acked)
	// Output:
	// from documents, key note-0193, attempt 1
	// acked 1
}
