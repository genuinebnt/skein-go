package grpcapi

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

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/services/documents/internal/app"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/fakes"
)

const ws = "019c8cf2-1000-769b-b000-73aede463976"

// withUser stands in for grpcx's token check: the test sends the user ID in
// metadata and the interceptor puts verified claims in the context.
func withUser(ctx context.Context) context.Context {
	md, _ := metadata.FromIncomingContext(ctx)
	if users := md.Get("test-user"); len(users) == 1 {
		return auth.WithClaims(ctx, auth.Claims{Subject: auth.Subject{UserID: users[0]}}, "token")
	}
	return ctx
}

type wrapped struct {
	grpc.ServerStream
	ctx context.Context
}

func (w wrapped) Context() context.Context { return w.ctx }

type harness struct {
	client skeinv1.DocumentServiceClient
	auth   *fakes.Authorizer
}

func start(t *testing.T) harness {
	t.Helper()
	authz := fakes.NewAuthorizer()
	authz.AllowCreate(ws, "alice")
	clock := fakes.NewClock(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	svc := app.New(app.Deps{Store: fakes.NewStore(), Authorizer: authz, Replicas: fakes.NewReplicas(), NoteCache: fakes.NewNoteCache(),
		Presence: fakes.NewPresence(clock), Clock: clock}, app.Config{Tick: time.Hour})
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			return h(withUser(ctx), req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			return h(srv, wrapped{ss, withUser(ss.Context())})
		}))
	skeinv1.RegisterDocumentServiceServer(srv, New(svc, nil))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return harness{client: skeinv1.NewDocumentServiceClient(conn), auth: authz}
}

func as(user string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "test-user", user)
}

func TestUnaryRPCsMapRequestsAndErrors(t *testing.T) {
	h := start(t)
	created, err := h.client.CreateNote(as("alice"), &skeinv1.CreateNoteRequest{WorkspaceId: ws, Title: "Raft", Markdown: "# Raft\nlog"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetNote().GetId()
	h.auth.SetRole(ws, domain.NoteID(id), "alice", fakes.Owner)
	got, err := h.client.GetNote(as("alice"), &skeinv1.GetNoteRequest{NoteId: id, IncludeMarkdown: true})
	if err != nil || got.GetMarkdown() != "# Raft\n\nlog" || len(got.GetVersion().GetEntries()) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	_, err = h.client.GetNote(as("mallory"), &skeinv1.GetNoteRequest{NoteId: id})
	st := status.Convert(err)
	if st.Code() != codes.NotFound || len(st.Details()) == 0 {
		t.Fatalf("status %v details %v", st.Code(), st.Details())
	}
	if info, ok := st.Details()[0].(*errdetails.ResourceInfo); !ok || info.GetResourceName() != id {
		t.Fatalf("details %v", st.Details())
	}
	if _, err := h.client.GetNote(as("alice"), &skeinv1.GetNoteRequest{NoteId: "not-a-uuid"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := h.client.GetNote(context.Background(), &skeinv1.GetNoteRequest{NoteId: id}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no caller: %v", err)
	}
	dup := &skeinv1.VersionVector{Entries: []*skeinv1.VersionEntry{{ReplicaId: 2, Counter: 1}, {ReplicaId: 2, Counter: 1}}}
	if _, err := h.client.GetTextAt(as("alice"), &skeinv1.GetTextAtRequest{NoteId: id, Version: dup}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("duplicate replica: %v", err)
	}
	if _, err := h.client.Undo(as("alice"), &skeinv1.UndoRequest{NoteId: id, ReplicaId: 1 << 40}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nothing to undo: %v", err)
	}
	hunks, err := h.client.Diff(as("alice"), &skeinv1.DiffRequest{NoteId: id, From: &skeinv1.VersionVector{}, To: got.GetVersion()})
	if err != nil || len(hunks.GetHunks()) != 1 || hunks.GetHunks()[0].GetKind() != skeinv1.DiffHunk_KIND_INSERT {
		t.Fatalf("%v %v", hunks, err)
	}
}

func TestSyncStreamsOpsBetweenClientsAndRejectsAViewer(t *testing.T) {
	h := start(t)
	created, err := h.client.CreateNote(as("alice"), &skeinv1.CreateNoteRequest{WorkspaceId: ws, Title: "Raft"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetNote().GetId()
	h.auth.SetRole(ws, domain.NoteID(id), "alice", fakes.Editor)
	h.auth.SetRole(ws, domain.NoteID(id), "victor", fakes.Viewer)

	open := func(user string, replica uint64) skeinv1.DocumentService_SyncClient {
		stream, err := h.client.Sync(as(user))
		if err != nil {
			t.Fatal(err)
		}
		if err := stream.Send(&skeinv1.SyncRequest{Message: &skeinv1.SyncRequest_Hello{Hello: &skeinv1.SyncHello{NoteId: id, ReplicaId: replica}}}); err != nil {
			t.Fatal(err)
		}
		return stream
	}
	writer := open("alice", 1<<40)
	reader := open("victor", 1<<41)
	time.Sleep(50 * time.Millisecond) // both sessions registered before the edit

	seq, _ := crdt.New(1 << 40)
	ops, _ := seq.Insert(0, "\u001ehello")
	if err := writer.Send(&skeinv1.SyncRequest{Message: &skeinv1.SyncRequest_Ops{Ops: crdt.EncodeOps(ops)}}); err != nil {
		t.Fatal(err)
	}
	got, err := reader.Recv()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := crdt.DecodeOps(got.GetOps())
	if err != nil || len(decoded) != 6 {
		t.Fatalf("reader got %v %v", got, err)
	}

	viewerSeq, _ := crdt.New(1 << 41)
	for _, op := range decoded {
		_ = viewerSeq.Apply(op)
	}
	mine, _ := viewerSeq.Insert(6, "!")
	if err := reader.Send(&skeinv1.SyncRequest{Message: &skeinv1.SyncRequest_Ops{Ops: crdt.EncodeOps(mine)}}); err != nil {
		t.Fatal(err)
	}
	rej, err := reader.Recv()
	if err != nil || rej.GetRejected() == nil || rej.GetRejected().GetReason() != app.ReasonInsufficientRole {
		t.Fatalf("viewer's edit: %v %v", rej, err)
	}

	bad, err := h.client.Sync(as("alice"))
	if err != nil {
		t.Fatal(err)
	}
	_ = bad.Send(&skeinv1.SyncRequest{Message: &skeinv1.SyncRequest_Ops{Ops: []byte{1}}})
	if _, err := bad.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("frame before hello: %v", err)
	}
	_ = writer.CloseSend()
	_ = reader.CloseSend()
}
