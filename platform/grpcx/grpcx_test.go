package grpcx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/ratelimit"
)

// echoService is a hand-written service so the tests need no generated
// code: Echo returns what the handler saw, Stream sends it once.
type echoService struct {
	failures atomic.Int32 // Echo fails with UNAVAILABLE while positive
	calls    atomic.Int32
	block    chan struct{} // when set, Echo waits for it to close
}

// observed is what a handler reports about its context.
type observed struct {
	RequestID   string `json:"request_id"`
	UserID      string `json:"user_id"`
	AgentRunID  string `json:"agent_run_id"`
	HasDeadline bool   `json:"has_deadline"`
	OutgoingMD  string `json:"outgoing_md"`
}

func observe(ctx context.Context) string {
	o := observed{RequestID: logging.RequestID(ctx)}
	if s, ok := auth.SubjectFromContext(ctx); ok {
		o.UserID, o.AgentRunID = s.UserID, s.AgentRunID
	}
	_, o.HasDeadline = ctx.Deadline()
	data, _ := json.Marshal(o)
	return string(data)
}

func (e *echoService) echo(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
	e.calls.Add(1)
	if e.block != nil {
		<-e.block
	}
	if e.failures.Add(-1) >= 0 {
		return nil, status.Error(codes.Unavailable, "try again")
	}
	if in.GetValue() == "panic" {
		panic("boom")
	}
	return wrapperspb.String(observe(ctx)), nil
}

var echoDesc = grpc.ServiceDesc{
	ServiceName: "skein.test.Echo",
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Echo", Handler: echoHandler("/skein.test.Echo/Echo")},
		{MethodName: "Create", Handler: echoHandler("/skein.test.Echo/Create")},
		{MethodName: "Public", Handler: echoHandler("/skein.test.Echo/Public")},
	},
	Streams: []grpc.StreamDesc{{StreamName: "Stream", ServerStreams: true, Handler: func(srv any, stream grpc.ServerStream) error {
		var in wrapperspb.StringValue
		if err := stream.RecvMsg(&in); err != nil {
			return err
		}
		return stream.SendMsg(wrapperspb.String(observe(stream.Context())))
	}}},
}

func echoHandler(fullMethod string) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := new(wrapperspb.StringValue)
		if err := dec(in); err != nil {
			return nil, err
		}
		handler := func(ctx context.Context, req any) (any, error) {
			return srv.(*echoService).echo(ctx, req.(*wrapperspb.StringValue))
		}
		return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: fullMethod}, handler)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fixture struct {
	svc    *echoService
	srv    *Server
	conn   *grpc.ClientConn
	logs   *syncBuffer
	signer *auth.Signer
	cancel context.CancelFunc
	done   chan error
}

func newFixture(t *testing.T, cfg ServerConfig, client ClientConfig) *fixture {
	t.Helper()
	logs := &syncBuffer{}
	key, _ := auth.GenerateKey()
	signer := auth.NewSigner(key, nil)
	if cfg.Logger == nil {
		cfg.Logger = logging.New(logging.Options{Service: "test", Writer: logs, Level: slog.LevelDebug})
	}
	if cfg.Verifier == nil {
		cfg.Verifier = auth.NewVerifier(auth.NewKeySet(signer.PublicKey()), auth.VerifierOptions{})
	}
	if cfg.PublicMethods == nil {
		cfg.PublicMethods = []string{"/skein.test.Echo/Public"}
	}
	svc := &echoService{}
	srv := NewServer(cfg)
	srv.RegisterService(&echoDesc, svc)

	lis := bufconn.Listen(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, lis) }()

	client.Target = "passthrough:///bufnet"
	client.Insecure = true
	client.Options = append(client.Options, grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}))
	conn, err := NewClient(client)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{svc: svc, srv: srv, conn: conn, logs: logs, signer: signer, cancel: cancel, done: done}
	t.Cleanup(func() {
		_ = conn.Close()
		cancel()
		<-done
	})
	return f
}

func (f *fixture) token(t *testing.T, sub auth.Subject) string {
	t.Helper()
	tok, _, err := f.signer.Issue(sub, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (f *fixture) call(ctx context.Context, method, value string, opts ...grpc.CallOption) (observed, error) {
	out := new(wrapperspb.StringValue)
	err := f.conn.Invoke(ctx, "/skein.test.Echo/"+method, wrapperspb.String(value), out, opts...)
	var o observed
	if err == nil {
		_ = json.Unmarshal([]byte(out.GetValue()), &o)
	}
	return o, err
}

func bearer(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func TestAuthenticatedCallSeesSubjectRequestIDAndDeadline(t *testing.T) {
	f := newFixture(t, ServerConfig{}, ClientConfig{})
	ctx := bearer(context.Background(), f.token(t, auth.Subject{UserID: "u1", AgentRunID: "run-1"}))
	var trailer metadata.MD
	got, err := f.call(ctx, "Echo", "hi", grpc.Trailer(&trailer))
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u1" || got.AgentRunID != "run-1" {
		t.Errorf("subject not in handler context: %+v", got)
	}
	if !logging.ValidRequestID(got.RequestID) || len(trailer.Get(RequestIDHeader)) != 1 || trailer.Get(RequestIDHeader)[0] != got.RequestID {
		t.Errorf("request id %q not generated and echoed (trailer %v)", got.RequestID, trailer)
	}
	if !got.HasDeadline {
		t.Error("default deadline not applied")
	}
	if !strings.Contains(f.logs.String(), `"user_id":"u1"`) || !strings.Contains(f.logs.String(), `"rpc.method":"/skein.test.Echo/Echo"`) {
		t.Errorf("call log missing fields: %s", f.logs.String())
	}
}

func TestValidIncomingRequestIDIsKept(t *testing.T) {
	f := newFixture(t, ServerConfig{}, ClientConfig{})
	ctx := logging.WithRequestID(context.Background(), "from-gateway-1")
	got, err := f.call(ctx, "Public", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "from-gateway-1" {
		t.Errorf("request id %q, want the propagated one", got.RequestID)
	}
	ctx = metadata.AppendToOutgoingContext(context.Background(), RequestIDHeader, "bad id!")
	if got, err = f.call(ctx, "Public", ""); err != nil || got.RequestID == "bad id!" || !logging.ValidRequestID(got.RequestID) {
		t.Errorf("invalid incoming id was kept: %q", got.RequestID)
	}
}

func TestMissingOrInvalidTokenIsUnauthenticated(t *testing.T) {
	f := newFixture(t, ServerConfig{}, ClientConfig{})
	stranger, _ := auth.GenerateKey()
	foreign, _, _ := auth.NewSigner(stranger, nil).Issue(auth.Subject{UserID: "u1"}, time.Hour)
	expired, _, _ := auth.NewSigner(stranger, func() time.Time { return time.Now().Add(-2 * time.Hour) }).Issue(auth.Subject{UserID: "u1"}, time.Minute)

	cases := map[string]context.Context{
		"no token":      context.Background(),
		"foreign key":   bearer(context.Background(), foreign),
		"garbage":       bearer(context.Background(), "not-a-jwt"),
		"basic scheme":  metadata.AppendToOutgoingContext(context.Background(), "authorization", "Basic dTpw"),
		"expired token": bearer(context.Background(), expired),
	}
	for name, ctx := range cases {
		if _, err := f.call(ctx, "Echo", ""); status.Code(err) != codes.Unauthenticated {
			t.Errorf("%s: want Unauthenticated, got %v", name, err)
		}
	}
	if f.svc.calls.Load() != 0 {
		t.Errorf("handler ran %d times for unauthenticated calls", f.svc.calls.Load())
	}
}

func TestPublicMethodsAndHealthNeedNoToken(t *testing.T) {
	f := newFixture(t, ServerConfig{}, ClientConfig{})
	if got, err := f.call(context.Background(), "Public", ""); err != nil || got.UserID != "" {
		t.Fatalf("public call: %v %+v", err, got)
	}
	// A valid token on a public method is still honoured.
	ctx := bearer(context.Background(), f.token(t, auth.Subject{UserID: "u2"}))
	if got, err := f.call(ctx, "Public", ""); err != nil || got.UserID != "u2" {
		t.Fatalf("public call with token: %v %+v", err, got)
	}
	resp, err := healthpb.NewHealthClient(f.conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health: %v %v", resp, err)
	}
}

func TestAuthFailsClosedWithoutVerifier(t *testing.T) {
	h := newFixtureNoVerifier(t)
	ctx := bearer(context.Background(), h.token(t, auth.Subject{UserID: "u1"}))
	if _, err := h.call(ctx, "Echo", ""); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("want Unauthenticated without a verifier, got %v", err)
	}
	if _, err := h.call(context.Background(), "Public", ""); err != nil {
		t.Fatalf("public method without verifier: %v", err)
	}
}

func newFixtureNoVerifier(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{}
	key, _ := auth.GenerateKey()
	f.signer = auth.NewSigner(key, nil)
	f.svc = &echoService{}
	f.srv = NewServer(ServerConfig{PublicMethods: []string{"/skein.test.Echo/Public"}})
	f.srv.RegisterService(&echoDesc, f.svc)
	lis := bufconn.Listen(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.srv.Serve(ctx, lis) }()
	conn, err := NewClient(ClientConfig{Target: "passthrough:///bufnet", Insecure: true, Options: []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.conn = conn
	t.Cleanup(func() { _ = conn.Close(); cancel(); <-done })
	return f
}

func TestPanicBecomesInternalAndIsLoggedWithStack(t *testing.T) {
	f := newFixture(t, ServerConfig{}, ClientConfig{})
	ctx := bearer(context.Background(), f.token(t, auth.Subject{UserID: "u1"}))
	_, err := f.call(logging.WithRequestID(ctx, "req-panic"), "Echo", "panic")
	if status.Code(err) != codes.Internal || status.Convert(err).Message() != "internal error" {
		t.Fatalf("want a bare Internal, got %v", err)
	}
	logs := f.logs.String()
	for _, want := range []string{`"panic":"boom"`, `"stack":"goroutine`, `"request_id":"req-panic"`, `"code":"Internal"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("panic log missing %s: %s", want, logs)
		}
	}
	if _, err := f.call(ctx, "Echo", "after"); err != nil {
		t.Fatalf("server did not survive the panic: %v", err)
	}
}

func TestClientDeadlineIsKeptAndMissingOneDefaulted(t *testing.T) {
	f := newFixture(t, ServerConfig{DefaultTimeout: -1}, ClientConfig{})
	ctx := bearer(context.Background(), f.token(t, auth.Subject{UserID: "u1"}))
	if got, _ := f.call(ctx, "Echo", ""); got.HasDeadline {
		t.Error("deadline set although the default is disabled")
	}
	withDeadline, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if got, _ := f.call(withDeadline, "Echo", ""); !got.HasDeadline {
		t.Error("client deadline lost")
	}
}

func TestRateLimitDenialCarriesRetryAfter(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	limiter := ratelimit.NewTokenBucket(clock)
	f := newFixture(t, ServerConfig{RateLimit: RateLimitBySubject(limiter, ratelimit.PerMinute(60, 2))}, ClientConfig{})
	ctx := bearer(context.Background(), f.token(t, auth.Subject{UserID: "u1"}))
	for range 2 {
		if _, err := f.call(ctx, "Echo", ""); err != nil {
			t.Fatal(err)
		}
	}
	_, err := f.call(ctx, "Echo", "")
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("want ResourceExhausted, got %v", err)
	}
	if d, ok := RetryAfter(err); !ok || d != time.Second {
		t.Fatalf("RetryAfter = %v %v, want 1s", d, ok)
	}
	// Another user has their own budget.
	other := bearer(context.Background(), f.token(t, auth.Subject{UserID: "u2"}))
	if _, err := f.call(other, "Echo", ""); err != nil {
		t.Fatalf("u2 limited by u1's budget: %v", err)
	}
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func TestRateLimiterFailureFailsOpen(t *testing.T) {
	failing := func(context.Context, string) (ratelimit.Decision, error) {
		return ratelimit.Decision{}, errors.New("redis down")
	}
	f := newFixture(t, ServerConfig{RateLimit: failing}, ClientConfig{})
	ctx := bearer(context.Background(), f.token(t, auth.Subject{UserID: "u1"}))
	if _, err := f.call(ctx, "Echo", ""); err != nil {
		t.Fatalf("limiter failure must not block calls: %v", err)
	}
	if !strings.Contains(f.logs.String(), "rate limiter failed") {
		t.Error("limiter failure not logged")
	}
}

func TestStreamsAreAuthenticatedAndCarryRequestID(t *testing.T) {
	f := newFixture(t, ServerConfig{}, ClientConfig{})
	desc := &grpc.StreamDesc{StreamName: "Stream", ServerStreams: true}
	open := func(ctx context.Context) (observed, error) {
		stream, err := f.conn.NewStream(ctx, desc, "/skein.test.Echo/Stream")
		if err != nil {
			return observed{}, err
		}
		if err := stream.SendMsg(wrapperspb.String("")); err != nil {
			return observed{}, err
		}
		_ = stream.CloseSend()
		out := new(wrapperspb.StringValue)
		if err := stream.RecvMsg(out); err != nil {
			return observed{}, err
		}
		var o observed
		_ = json.Unmarshal([]byte(out.GetValue()), &o)
		return o, nil
	}
	if _, err := open(context.Background()); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("stream without token: want Unauthenticated, got %v", err)
	}
	got, err := open(bearer(context.Background(), f.token(t, auth.Subject{UserID: "u1"})))
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "u1" || got.RequestID == "" || got.HasDeadline {
		t.Errorf("stream context %+v: want subject and request id, no default deadline", got)
	}
}

func TestClientForwardsRequestIDAndCallerToken(t *testing.T) {
	f := newFixture(t, ServerConfig{}, ClientConfig{})
	token := f.token(t, auth.Subject{UserID: "forwarded-user"})
	// As if this service is handling a call from that user and calls on.
	ctx := auth.WithClaims(context.Background(), auth.Claims{}, token)
	ctx = logging.WithRequestID(ctx, "chain-1")
	got, err := f.call(ctx, "Echo", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != "forwarded-user" || got.RequestID != "chain-1" {
		t.Errorf("not forwarded: %+v", got)
	}
}

func TestIdempotentMethodsAreRetriedOthersAreNot(t *testing.T) {
	f := newFixture(t, ServerConfig{}, ClientConfig{
		IdempotentMethods: []string{"skein.test.Echo/Echo"},
		Retry:             RetryPolicy{InitialBackoff: time.Millisecond, MaxBackoff: 5 * time.Millisecond},
	})
	ctx := bearer(context.Background(), f.token(t, auth.Subject{UserID: "u1"}))

	f.svc.failures.Store(2)
	f.svc.calls.Store(0)
	if _, err := f.call(ctx, "Echo", ""); err != nil {
		t.Fatalf("idempotent call not retried to success: %v", err)
	}
	if f.svc.calls.Load() != 3 {
		t.Errorf("Echo ran %d times, want 3", f.svc.calls.Load())
	}

	f.svc.failures.Store(1)
	f.svc.calls.Store(0)
	if _, err := f.call(ctx, "Create", ""); status.Code(err) != codes.Unavailable {
		t.Fatalf("non-idempotent call: want the UNAVAILABLE, got %v", err)
	}
	if f.svc.calls.Load() != 1 {
		t.Errorf("Create ran %d times, want exactly 1", f.svc.calls.Load())
	}
}

func TestServeDrainsInFlightCallsAndStopsServingHealth(t *testing.T) {
	f := newFixture(t, ServerConfig{}, ClientConfig{})
	ctx := bearer(context.Background(), f.token(t, auth.Subject{UserID: "u1"}))
	f.svc.block = make(chan struct{})
	result := make(chan error, 1)
	go func() { _, err := f.call(ctx, "Echo", ""); result <- err }()
	for f.svc.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	f.cancel()
	time.Sleep(20 * time.Millisecond)
	if resp, err := f.srv.Health.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("health during drain: %v %v", resp, err)
	}
	close(f.svc.block)
	if err := <-result; err != nil {
		t.Fatalf("in-flight call was cut off: %v", err)
	}
	if err := <-f.done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	f.done <- nil // for the cleanup
}

func TestServeForcesStopAfterTimeout(t *testing.T) {
	f := newFixture(t, ServerConfig{ShutdownTimeout: 50 * time.Millisecond}, ClientConfig{})
	ctx := bearer(context.Background(), f.token(t, auth.Subject{UserID: "u1"}))
	f.svc.block = make(chan struct{})
	defer close(f.svc.block)
	result := make(chan error, 1)
	go func() { _, err := f.call(ctx, "Echo", ""); result <- err }()
	for f.svc.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	f.cancel()
	if err := <-f.done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	f.done <- nil // for the cleanup
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("forced stop took %v", elapsed)
	}
	if err := <-result; status.Code(err) != codes.Unavailable {
		t.Fatalf("stuck call: want Unavailable after forced stop, got %v", err)
	}
}

func TestReflectionOnlyInDev(t *testing.T) {
	for env, want := range map[config.Env]bool{config.EnvDev: true, config.EnvTest: true, config.EnvProd: false} {
		srv := NewServer(ServerConfig{Env: env})
		_, has := srv.GetServiceInfo()["grpc.reflection.v1.ServerReflection"]
		if has != want {
			t.Errorf("env %s: reflection registered = %v", env, has)
		}
	}
}

func TestServerSpansAreRecorded(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	f := newFixture(t, ServerConfig{TracerProvider: tp}, ClientConfig{})
	if _, err := f.call(context.Background(), "Public", ""); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range spans.GetSpans() {
		names = append(names, s.Name)
	}
	if len(names) != 1 || names[0] != "skein.test.Echo/Public" {
		t.Fatalf("server spans %v", names)
	}
}

func TestServiceConfigJSON(t *testing.T) {
	got, err := ServiceConfigJSON([]string{"skein.v1.SearchService", "/skein.v1.IdentityService/Authorize"}, RetryPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"loadBalancingConfig":[{"round_robin":{}}],"methodConfig":[{"name":[{"service":"skein.v1.SearchService"},{"service":"skein.v1.IdentityService","method":"Authorize"}],"retryPolicy":{"maxAttempts":4,"initialBackoff":"0.1s","maxBackoff":"2s","backoffMultiplier":2,"retryableStatusCodes":["UNAVAILABLE"]}}]}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if _, err := ServiceConfigJSON([]string{"a/b/c"}, RetryPolicy{}); !errors.Is(err, ErrInvalidClientConfig) {
		t.Errorf("bad method name: %v", err)
	}
	if _, err := NewClient(ClientConfig{}); !errors.Is(err, ErrInvalidClientConfig) {
		t.Errorf("empty target: %v", err)
	}
	// gRPC itself must accept the generated config.
	conn, err := NewClient(ClientConfig{Target: "dns:///localhost:1", Insecure: true, IdempotentMethods: []string{"skein.v1.SearchService"},
		Retry: RetryPolicy{InitialBackoff: 50 * time.Microsecond}})
	if err != nil {
		t.Fatalf("grpc rejected the service config: %v", err)
	}
	_ = conn.Close()
}
