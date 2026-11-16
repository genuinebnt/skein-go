//go:build integration

package events

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const emulatorProject = "skein-it"

// startEmulator runs the same emulator image as deploy/compose and points
// the client library at it through PUBSUB_EMULATOR_HOST, the documented
// local path.
func startEmulator(t *testing.T) *PubSub {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "gcr.io/google.com/cloudsdktool/google-cloud-cli:emulators",
			Cmd:          []string{"gcloud", "beta", "emulators", "pubsub", "start", "--host-port=0.0.0.0:8085", "--project=" + emulatorProject},
			ExposedPorts: []string{"8085/tcp"},
			WaitingFor:   wait.ForLog("Server started").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start emulator: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	endpoint, err := container.PortEndpoint(ctx, "8085/tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PUBSUB_EMULATOR_HOST", endpoint)
	ps, err := NewPubSub(ctx, PubSubConfig{Project: emulatorProject})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return ps
}

func TestEmulatorDeadLettersAfterMaxAttempts(t *testing.T) {
	ps := startEmulator(t)
	ctx := context.Background()
	project := "projects/" + emulatorProject
	for _, topic := range []string{"documents.events", "documents.events.dead-letter"} {
		if _, err := ps.client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: project + "/topics/" + topic}); err != nil {
			t.Fatal(err)
		}
	}
	for _, sub := range []*pubsubpb.Subscription{
		{
			Name: project + "/subscriptions/search.documents-events", Topic: project + "/topics/documents.events",
			EnableMessageOrdering: true, AckDeadlineSeconds: 10,
			DeadLetterPolicy: &pubsubpb.DeadLetterPolicy{DeadLetterTopic: project + "/topics/documents.events.dead-letter", MaxDeliveryAttempts: 5},
			RetryPolicy:      &pubsubpb.RetryPolicy{},
		},
		{Name: project + "/subscriptions/dead-letters", Topic: project + "/topics/documents.events.dead-letter", AckDeadlineSeconds: 10},
	} {
		if _, err := ps.client.SubscriptionAdminClient.CreateSubscription(ctx, sub); err != nil {
			t.Fatal(err)
		}
	}

	poison := envelope(t, "n1", "poison")
	if err := ps.Publish(ctx, "documents.events", poison); err != nil {
		t.Fatal(err)
	}
	var lastAttempt atomic.Int32
	consumeCtx, stop := context.WithTimeout(ctx, 60*time.Second)
	defer stop()
	go func() {
		_ = ps.Subscribe(consumeCtx, "search.documents-events", func(_ context.Context, msg Message) error {
			lastAttempt.Store(int32(msg.DeliveryAttempt))
			return errors.New("always fails")
		})
	}()
	var dead Message
	err := ps.Subscribe(consumeCtx, "dead-letters", func(_ context.Context, msg Message) error {
		dead = msg
		stop()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if dead.ID != poison.ID || string(dead.Payload) != "poison" {
		t.Fatalf("dead-letter topic got %+v, want the poison envelope", dead.Envelope)
	}
	if lastAttempt.Load() < 1 {
		t.Errorf("DeliveryAttempt was never reported (last %d)", lastAttempt.Load())
	}
}
