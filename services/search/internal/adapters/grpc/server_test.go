package grpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/platform/redisx"
	grpcadapter "github.com/genuinebnt/skein-go/services/search/internal/adapters/grpc"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/internal/fakes"
)

// startServer serves the handlers through grpcx's real interceptor chain
// (token verification included) on an in-memory listener.
func startServer(t *testing.T) (skeinv1.SearchServiceClient, skeinv1.GraphServiceClient, context.Context) {
	t.Helper()
	store, dir, cache := fakes.NewStore(), fakes.NewDirectory(), redisx.NewMemoryStore(time.Now)
	index := app.NewIndex(store, app.IndexConfig{})
	projector := app.NewProjector(store, index, nil, nil)
	send := func(i int, e *skeinv1.Event) {
		data, _ := proto.Marshal(e)
		if _, err := projector.HandleDocuments(context.Background(), events.Message{Envelope: events.Envelope{ID: string(rune('a' + i)), Payload: data}}); err != nil {
			t.Fatal(err)
		}
	}
	send(0, &skeinv1.Event{Payload: &skeinv1.Event_NoteCreated{NoteCreated: &skeinv1.NoteCreated{NoteId: "a", WorkspaceId: "w", Title: "Raft log compaction"}}})
	send(1, &skeinv1.Event{Payload: &skeinv1.Event_BlocksChanged{BlocksChanged: &skeinv1.BlocksChanged{NoteId: "a",
		Blocks: []*skeinv1.ChangedBlock{{BlockId: "1", Kind: "code", Language: "go", Text: "func Snapshot(ctx context.Context, upTo uint64) error {"}}}}})
	send(2, &skeinv1.Event{Payload: &skeinv1.Event_NoteCreated{NoteCreated: &skeinv1.NoteCreated{NoteId: "secret", WorkspaceId: "w", Title: "Secret"}}})
	send(3, &skeinv1.Event{Payload: &skeinv1.Event_LinksChanged{LinksChanged: &skeinv1.LinksChanged{NoteId: "secret", Targets: []string{"a"}}}})
	if err := index.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	dir.Grant("alice", "w", "a")
	access, _ := app.NewAccess(dir, cache)
	searcher, _ := app.NewSearcher(index, access, cache)
	grapher := app.NewGrapher(index, access, app.NewAnalyzer(app.AnalyzerConfig{}), nil, nil, nil)

	key, _ := auth.GenerateKey()
	signer := auth.NewSigner(key, nil)
	srv := grpcx.NewServer(grpcx.ServerConfig{Verifier: auth.NewVerifier(auth.NewKeySet(signer.PublicKey()), auth.VerifierOptions{})})
	grpcadapter.NewServer(searcher, grapher).Register(srv)
	lis := bufconn.Listen(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = srv.Serve(ctx, lis); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	token, _, err := signer.Issue(auth.Subject{UserID: "alice"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	callCtx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
	return skeinv1.NewSearchServiceClient(conn), skeinv1.NewGraphServiceClient(conn), callCtx
}

func TestSearchOverGRPCReturnsTheDocumentedShape(t *testing.T) {
	search, _, ctx := startServer(t)
	resp, err := search.Search(ctx, &skeinv1.SearchRequest{WorkspaceId: "w", Query: `func \w+\(ctx context\.Context`,
		Mode: skeinv1.SearchMode_SEARCH_MODE_REGEX, Languages: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Hits) != 1 || resp.Matched != 1 || resp.Candidates != 1 {
		t.Fatalf("response %v", resp)
	}
	h := resp.Hits[0]
	if h.Location.NoteId != "a" || h.Location.End != 33 || h.NoteTitle != "Raft log compaction" || h.Language != "go" || h.Score != 1 ||
		len(h.Highlights) != 1 || h.Highlights[0].End != 33 {
		t.Fatalf("hit %v", h)
	}
}

func TestErrorsCarryTheDocumentedDetails(t *testing.T) {
	search, graphs, ctx := startServer(t)
	_, err := search.Search(ctx, &skeinv1.SearchRequest{WorkspaceId: "w", Query: "(", Mode: skeinv1.SearchMode_SEARCH_MODE_REGEX})
	st := status.Convert(err)
	if st.Code() != codes.InvalidArgument || len(st.Details()) != 2 {
		t.Fatalf("bad regex: %v", st)
	}
	if br, ok := st.Details()[0].(*errdetails.BadRequest); !ok || br.FieldViolations[0].Field != "query" {
		t.Fatalf("details %v", st.Details())
	}
	_, err = graphs.BlastRadius(ctx, &skeinv1.BlastRadiusRequest{NoteId: "secret"})
	st = status.Convert(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("unreadable note: %v (must be NOT_FOUND, not PERMISSION_DENIED)", st)
	}
	if ri, ok := st.Details()[0].(*errdetails.ResourceInfo); !ok || ri.ResourceName != "secret" {
		t.Fatalf("details %v", st.Details())
	}
	_, err = search.Search(context.Background(), &skeinv1.SearchRequest{WorkspaceId: "w", Query: "raft"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("call without a token: %v", err)
	}
}

func TestAnalyzeGraphReportsItsVersionsInHeaders(t *testing.T) {
	_, graphs, ctx := startServer(t)
	var header metadata.MD
	resp, err := graphs.AnalyzeGraph(ctx, &skeinv1.AnalyzeGraphRequest{WorkspaceId: "w"}, grpc.Header(&header))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ComponentOf) != 1 || header.Get(grpcadapter.HeaderGraphVersion)[0] != "4" || header.Get(grpcadapter.HeaderMetricsPending)[0] != "false" {
		t.Fatalf("response %v header %v: the unreadable note must not appear", resp, header)
	}
}
