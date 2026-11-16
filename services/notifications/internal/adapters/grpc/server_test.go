package grpc

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	gogrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
	"github.com/genuinebnt/skein-go/services/notifications/internal/fakes"
)

func uid(n int) string { return fmt.Sprintf("01930000-0000-7000-8000-%012x", n) }

var ada, grace = uid(1), uid(2)

type rig struct {
	t        *testing.T
	client   skeinv1.NotificationServiceClient
	signer   *auth.Signer
	producer *app.Producer
	hub      *app.Hub
	events   int
}

func newRig(t *testing.T, buffer int) *rig {
	t.Helper()
	key, err := auth.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer := auth.NewSigner(key, time.Now)
	store, cache, bus := fakes.NewStore(), fakes.NewUnreadCache(), &fakes.FanoutBus{}
	fanout := bus.Replica()
	hub, err := app.NewHub(fanout, buffer, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fanout.SetDeliver(hub.Deliver)
	producer, err := app.NewProducer(app.ProducerConfig{Store: store, Authorizer: fakes.AllowAll(), Directory: fakes.NewDirectory(),
		UnreadCache: cache, Fanout: fanout, NewID: fakes.IDs(1)})
	if err != nil {
		t.Fatal(err)
	}
	srv := grpcx.NewServer(grpcx.ServerConfig{Verifier: auth.NewVerifier(auth.NewKeySet(signer.PublicKey()), auth.VerifierOptions{})})
	New(app.NewInbox(store, cache, nil, nil), hub, nil).Register(srv)
	lis := bufconn.Listen(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, lis); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	conn, err := gogrpc.NewClient("passthrough:///bufnet",
		gogrpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		gogrpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &rig{t: t, client: skeinv1.NewNotificationServiceClient(conn), signer: signer, producer: producer, hub: hub}
}

func (r *rig) as(user string) context.Context {
	r.t.Helper()
	token, _, err := r.signer.Issue(auth.Subject{UserID: user}, time.Minute)
	if err != nil {
		r.t.Fatal(err)
	}
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}

func (r *rig) notify(user, title string) {
	r.t.Helper()
	r.events++
	ev := app.Event{Consumer: "test", ID: uid(1000 + r.events), OccurredAt: time.Date(2026, 11, 16, 9, r.events, 0, 0, time.UTC), ActorUserID: grace}
	if _, err := r.producer.Request(context.Background(), ev, domain.Request{RecipientID: user, Kind: domain.KindMention, Title: title,
		Location: domain.Location{NoteID: uid(500), BlockID: "1:2"}}); err != nil {
		r.t.Fatal(err)
	}
}

func code(err error) codes.Code { return status.Code(err) }

func TestEveryRPCNeedsAnAuthenticatedCaller(t *testing.T) {
	r := newRig(t, 4)
	ctx := context.Background()
	_, err := r.client.ListInbox(ctx, &skeinv1.ListInboxRequest{})
	if code(err) != codes.Unauthenticated {
		t.Fatalf("ListInbox without a token: %v", err)
	}
	stream, err := r.client.Subscribe(ctx, &skeinv1.SubscribeRequest{})
	if err == nil {
		_, err = stream.Recv()
	}
	if code(err) != codes.Unauthenticated {
		t.Fatalf("Subscribe without a token: %v", err)
	}
}

func TestListInboxAndMarkReadActOnTheCallersOwnInbox(t *testing.T) {
	r := newRig(t, 4)
	r.notify(ada, "for ada")
	r.notify(grace, "for grace")
	resp, err := r.client.ListInbox(r.as(ada), &skeinv1.ListInboxRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Notifications) != 1 || resp.Unread != 1 {
		t.Fatalf("ada sees %v", resp)
	}
	n := resp.Notifications[0]
	if n.Title != "for ada" || n.Kind != skeinv1.NotificationKind_NOTIFICATION_KIND_MENTION || n.GetLocation().GetBlockId() != "1:2" || n.ActorId != grace {
		t.Errorf("notification %v", n)
	}
	graceInbox, _ := r.client.ListInbox(r.as(grace), &skeinv1.ListInboxRequest{})
	if _, err := r.client.MarkRead(r.as(ada), &skeinv1.MarkReadRequest{Ids: []string{graceInbox.Notifications[0].Id}}); err != nil {
		t.Fatal(err)
	}
	if again, _ := r.client.ListInbox(r.as(grace), &skeinv1.ListInboxRequest{UnreadOnly: true}); len(again.Notifications) != 1 {
		t.Fatal("ada marked grace's notification read")
	}
	if _, err := r.client.MarkRead(r.as(ada), &skeinv1.MarkReadRequest{Ids: []string{n.Id}}); err != nil {
		t.Fatal(err)
	}
	if after, _ := r.client.ListInbox(r.as(ada), &skeinv1.ListInboxRequest{UnreadOnly: true}); len(after.Notifications) != 0 || after.Unread != 0 {
		t.Fatalf("after MarkRead: %v", after)
	}
	if _, err := r.client.MarkRead(r.as(ada), &skeinv1.MarkReadRequest{}); code(err) != codes.InvalidArgument {
		t.Fatalf("MarkRead without ids: %v", err)
	}
}

func TestPagingOverGRPC(t *testing.T) {
	r := newRig(t, 4)
	for i := range 5 {
		r.notify(ada, fmt.Sprint("n", i))
	}
	var titles []string
	req := &skeinv1.ListInboxRequest{Page: &skeinv1.PageRequest{PageSize: 2}}
	for {
		resp, err := r.client.ListInbox(r.as(ada), req)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range resp.Notifications {
			titles = append(titles, n.Title)
		}
		if resp.GetPage().GetNextPageToken() == "" {
			break
		}
		req.Page.PageToken = resp.Page.NextPageToken
	}
	if fmt.Sprint(titles) != "[n4 n3 n2 n1 n0]" {
		t.Fatalf("paged %v", titles)
	}
	_, err := r.client.ListInbox(r.as(ada), &skeinv1.ListInboxRequest{Page: &skeinv1.PageRequest{PageToken: "nope"}})
	if code(err) != codes.InvalidArgument {
		t.Fatalf("bad token: %v", err)
	}
}

func TestPreferencesOverGRPC(t *testing.T) {
	r := newRig(t, 4)
	got, err := r.client.GetPreferences(r.as(ada), &skeinv1.GetPreferencesRequest{})
	if err != nil || len(got.Preferences) != 5 || !got.Preferences[0].Email || got.Preferences[1].Email {
		t.Fatalf("defaults %v, %v", got, err)
	}
	comment := skeinv1.NotificationKind_NOTIFICATION_KIND_COMMENT
	if _, err := r.client.UpdatePreferences(r.as(ada), &skeinv1.UpdatePreferencesRequest{Preferences: []*skeinv1.Preference{
		{Kind: comment, InApp: true, Email: true}}}); err != nil {
		t.Fatal(err)
	}
	got, _ = r.client.GetPreferences(r.as(ada), &skeinv1.GetPreferencesRequest{})
	if !got.Preferences[1].Email {
		t.Fatal("update not applied")
	}
	for name, prefs := range map[string][]*skeinv1.Preference{
		"duplicate":    {{Kind: comment}, {Kind: comment}},
		"unspecified":  {{Kind: skeinv1.NotificationKind_NOTIFICATION_KIND_UNSPECIFIED}},
		"unknown":      {{Kind: 77}},
		"wraps a byte": {{Kind: 257}},
	} {
		if _, err := r.client.UpdatePreferences(r.as(ada), &skeinv1.UpdatePreferencesRequest{Preferences: prefs}); code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSubscribeStreamsTheCallersNewNotifications(t *testing.T) {
	r := newRig(t, 4)
	ctx, cancel := context.WithCancel(r.as(ada))
	defer cancel()
	stream, err := r.client.Subscribe(ctx, &skeinv1.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	waitForSubscriber(t, r.hub, ada)
	r.notify(grace, "not for ada")
	r.notify(ada, "live")
	resp, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if resp.Notification.Title != "live" {
		t.Fatalf("received %v", resp.Notification)
	}
}

func TestASlowSubscriberIsToldToResubscribe(t *testing.T) {
	r := newRig(t, 1)
	stream, err := r.client.Subscribe(r.as(ada), &skeinv1.SubscribeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	waitForSubscriber(t, r.hub, ada)
	deadline := time.Now().Add(10 * time.Second)
	// The client reads nothing while notifications pour in: HTTP/2 flow
	// control fills, the server's Send blocks, the hub's buffer overflows.
	// Delivering through the hub directly keeps the flood fast.
	big := domain.Notification{ID: uid(9), RecipientID: ada, Kind: domain.KindMention, Title: "flood",
		Body: string(make([]byte, 1000)), CreatedAt: time.Now().UTC()}
	for r.hub.Subscribers(ada) > 0 && time.Now().Before(deadline) {
		r.hub.Deliver(big)
	}
	for {
		if _, err := stream.Recv(); err != nil {
			if code(err) != codes.ResourceExhausted {
				t.Fatalf("stream ended with %v", err)
			}
			return
		}
	}
}

// waitForSubscriber waits until the server has registered the stream.
func waitForSubscriber(t *testing.T, hub *app.Hub, user string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for hub.Subscribers(user) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("subscription never registered")
		}
		time.Sleep(time.Millisecond)
	}
}
