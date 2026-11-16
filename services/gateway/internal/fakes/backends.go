package fakes

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/grpcx"

	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/backends"
	"github.com/genuinebnt/skein-go/services/gateway/internal/app"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// Call is one RPC a fake backend received.
type Call struct {
	Method  string
	MD      metadata.MD
	Request proto.Message
}

// UnaryFunc answers one unary call.
type UnaryFunc func(ctx context.Context, req proto.Message) (proto.Message, error)

// Backends is every gateway backend in one in-process gRPC server.
type Backends struct {
	// Conns connect to the server through platform/grpcx clients, so the
	// gateway's real outgoing interceptors (token, request ID) run.
	Conns backends.Conns

	mu          sync.Mutex
	calls       []Call
	unary       map[string]UnaryFunc
	deniedNotes map[string]bool
	sendMessage func(req *skeinv1.SendMessageRequest, stream skeinv1.AgentService_SendMessageServer) error
	subscribe   func(stream skeinv1.NotificationService_SubscribeServer) error
}

type services struct {
	skeinv1.UnimplementedIdentityServiceServer
	skeinv1.UnimplementedDocumentServiceServer
	skeinv1.UnimplementedSearchServiceServer
	skeinv1.UnimplementedGraphServiceServer
	skeinv1.UnimplementedMLServiceServer
	skeinv1.UnimplementedAgentServiceServer
	skeinv1.UnimplementedNotificationServiceServer
	b *Backends
}

// StartBackends starts the fake and stops it when t ends.
func StartBackends(t testing.TB) *Backends {
	t.Helper()
	b := &Backends{unary: map[string]UnaryFunc{}, deniedNotes: map[string]bool{}}
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(b.interceptUnary), grpc.ChainStreamInterceptor(b.interceptStream))
	s := &services{b: b}
	skeinv1.RegisterIdentityServiceServer(srv, &s.UnimplementedIdentityServiceServer)
	skeinv1.RegisterDocumentServiceServer(srv, &s.UnimplementedDocumentServiceServer)
	skeinv1.RegisterSearchServiceServer(srv, &s.UnimplementedSearchServiceServer)
	skeinv1.RegisterGraphServiceServer(srv, &s.UnimplementedGraphServiceServer)
	skeinv1.RegisterMLServiceServer(srv, &s.UnimplementedMLServiceServer)
	skeinv1.RegisterAgentServiceServer(srv, agentServer{s})
	skeinv1.RegisterNotificationServiceServer(srv, notificationServer{s})
	conn := serveBufconn(t, srv)
	b.Conns = backends.Conns{Identity: conn, Documents: conn, Search: conn, ML: conn, Agent: conn, Notifications: conn}
	return b
}

// serveBufconn serves srv on an in-memory listener and returns a grpcx
// client connection to it.
func serveBufconn(t testing.TB, srv *grpc.Server) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpcx.NewClient(grpcx.ClientConfig{
		Target: "passthrough:///bufnet", Insecure: true, IdempotentMethods: backends.IdempotentMethods,
		Options: []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(); srv.Stop() })
	return conn
}

// OnUnary sets the answer for one unary RPC ("/skein.v1.X/Y"). Without
// one, a call gets an empty response of the right type (Authorize gets
// allowed for every check not denied with DenyNote).
func (b *Backends) OnUnary(method string, f UnaryFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.unary[method] = f
}

// DenyNote makes Authorize refuse every check on noteID.
func (b *Backends) DenyNote(noteID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deniedNotes[noteID] = true
}

// OnSendMessage replaces the default agent run (one text delta echoing the
// message, then finished).
func (b *Backends) OnSendMessage(f func(req *skeinv1.SendMessageRequest, stream skeinv1.AgentService_SendMessageServer) error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sendMessage = f
}

// OnSubscribe replaces the default notifications stream (which waits for
// the client to go away).
func (b *Backends) OnSubscribe(f func(stream skeinv1.NotificationService_SubscribeServer) error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribe = f
}

// Calls returns every call received so far.
func (b *Backends) Calls() []Call {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Call(nil), b.calls...)
}

// CallsTo returns the calls to one method.
func (b *Backends) CallsTo(method string) []Call {
	var out []Call
	for _, c := range b.Calls() {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func (b *Backends) record(ctx context.Context, method string, req proto.Message) {
	md, _ := metadata.FromIncomingContext(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, Call{Method: method, MD: md.Copy(), Request: req})
}

// interceptUnary answers every unary RPC without calling the embedded
// Unimplemented handler.
func (b *Backends) interceptUnary(ctx context.Context, req any, info *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
	msg := req.(proto.Message)
	b.record(ctx, info.FullMethod, msg)
	b.mu.Lock()
	f := b.unary[info.FullMethod]
	denied := make(map[string]bool, len(b.deniedNotes))
	for k, v := range b.deniedNotes {
		denied[k] = v
	}
	b.mu.Unlock()
	if f != nil {
		return f(ctx, msg)
	}
	if info.FullMethod == "/skein.v1.IdentityService/Authorize" {
		checks := msg.(*skeinv1.AuthorizeRequest).GetChecks()
		resp := &skeinv1.AuthorizeResponse{Allowed: make([]bool, len(checks))}
		for i, c := range checks {
			resp.Allowed[i] = !denied[c.GetResourceId()]
		}
		return resp, nil
	}
	return emptyResponse(info.FullMethod)
}

func (b *Backends) interceptStream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	b.record(ss.Context(), info.FullMethod, nil)
	return handler(srv, ss)
}

// emptyResponse makes the RPC's output message from the descriptors.
func emptyResponse(fullMethod string) (proto.Message, error) {
	serviceName, methodName, _ := strings.Cut(strings.TrimPrefix(fullMethod, "/"), "/")
	service, method := protoreflect.FullName(serviceName), protoreflect.Name(methodName)
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(service)
	if err != nil {
		return nil, status.Errorf(codes.Unimplemented, "unknown service %s", service)
	}
	m := d.(protoreflect.ServiceDescriptor).Methods().ByName(method)
	if m == nil {
		return nil, status.Errorf(codes.Unimplemented, "unknown method %s", fullMethod)
	}
	mt, err := protoregistry.GlobalTypes.FindMessageByName(m.Output().FullName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "no type for %s", m.Output().FullName())
	}
	return mt.New().Interface(), nil
}

type agentServer struct{ *services }

func (a agentServer) SendMessage(req *skeinv1.SendMessageRequest, stream skeinv1.AgentService_SendMessageServer) error {
	a.b.mu.Lock()
	f := a.b.sendMessage
	a.b.mu.Unlock()
	if f != nil {
		return f(req, stream)
	}
	runID := "run-" + req.GetText()
	if err := stream.Send(&skeinv1.SendMessageResponse{RunId: runID, Event: &skeinv1.SendMessageResponse_TextDelta{TextDelta: "echo: " + req.GetText()}}); err != nil {
		return err
	}
	return stream.Send(&skeinv1.SendMessageResponse{RunId: runID, Event: &skeinv1.SendMessageResponse_Finished{
		Finished: &skeinv1.RunFinished{Outcome: skeinv1.RunFinished_OUTCOME_COMPLETED, Steps: 1}}})
}

type notificationServer struct{ *services }

func (n notificationServer) Subscribe(_ *skeinv1.SubscribeRequest, stream skeinv1.NotificationService_SubscribeServer) error {
	n.b.mu.Lock()
	f := n.b.subscribe
	n.b.mu.Unlock()
	if f != nil {
		return f(stream)
	}
	<-stream.Context().Done()
	return nil
}

// SyncFunc serves one Sync stream on a fake documents instance.
type SyncFunc func(instance string, stream skeinv1.DocumentService_SyncServer) error

// SyncCall is one Sync stream a fake documents instance received.
type SyncCall struct {
	Instance string
	MD       metadata.MD
}

// DocumentInstances are fake documents replicas, each its own gRPC server.
// Router routes notes over them with the real rendezvous ring.
type DocumentInstances struct {
	mu      sync.Mutex
	ring    domain.Ring
	clients map[string]skeinv1.DocumentServiceClient
	sync    SyncFunc
	calls   []SyncCall
}

type documentInstance struct {
	skeinv1.UnimplementedDocumentServiceServer
	name string
	d    *DocumentInstances
}

// StartDocumentInstances starts one fake documents server per name.
func StartDocumentInstances(t testing.TB, names ...string) *DocumentInstances {
	t.Helper()
	d := &DocumentInstances{ring: domain.NewRing(names), clients: map[string]skeinv1.DocumentServiceClient{}}
	for _, name := range names {
		srv := grpc.NewServer()
		skeinv1.RegisterDocumentServiceServer(srv, &documentInstance{name: name, d: d})
		d.clients[name] = skeinv1.NewDocumentServiceClient(serveBufconn(t, srv))
	}
	return d
}

// OnSync replaces the default stream, EchoSync.
func (d *DocumentInstances) OnSync(f SyncFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sync = f
}

// SetMembers changes which instances the router uses, as a scale-up or a
// pod replacement would.
func (d *DocumentInstances) SetMembers(names ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ring = domain.NewRing(names)
}

// Owner returns the instance that owns noteID.
func (d *DocumentInstances) Owner(noteID string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ring.Owner(noteID)
}

// Route implements app.SyncRouter.
func (d *DocumentInstances) Route(noteID string) (app.SyncRoute, error) {
	owner, ok := d.Owner(noteID)
	if !ok {
		return app.SyncRoute{}, status.Error(codes.Unavailable, "no documents instance")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return app.SyncRoute{Owner: owner, Client: d.clients[owner]}, nil
}

// SyncCalls returns every Sync stream received so far.
func (d *DocumentInstances) SyncCalls() []SyncCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]SyncCall(nil), d.calls...)
}

func (i *documentInstance) Sync(stream skeinv1.DocumentService_SyncServer) error {
	md, _ := metadata.FromIncomingContext(stream.Context())
	i.d.mu.Lock()
	i.d.calls = append(i.d.calls, SyncCall{Instance: i.name, MD: md.Copy()})
	f := i.d.sync
	i.d.mu.Unlock()
	if f == nil {
		f = EchoSync
	}
	return f(i.name, stream)
}

// EchoSync answers every ops or presence frame with the same frame, and
// ends when the client half-closes.
func EchoSync(_ string, stream skeinv1.DocumentService_SyncServer) error {
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch m := req.GetMessage().(type) {
		case *skeinv1.SyncRequest_Ops:
			err = stream.Send(&skeinv1.SyncResponse{Message: &skeinv1.SyncResponse_Ops{Ops: m.Ops}})
		case *skeinv1.SyncRequest_Presence:
			err = stream.Send(&skeinv1.SyncResponse{Message: &skeinv1.SyncResponse_Presence{Presence: m.Presence}})
		}
		if err != nil {
			return err
		}
	}
}
