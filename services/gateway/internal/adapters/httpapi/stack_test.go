package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/httpx"
	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/backends"
	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/edge"
	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/wsapi"
	"github.com/genuinebnt/skein-go/services/gateway/internal/app"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
	"github.com/genuinebnt/skein-go/services/gateway/internal/fakes"
)

// fakeClock drives the rate limiter.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

// syncBuffer collects log output from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

type stackOptions struct {
	limits    domain.Limits
	limiter   ratelimit.Limiter
	ws        wsapi.Options
	cors      httpx.CORSConfig
	drain     time.Duration
	instances []string
}

// stack is the whole gateway in process, with fake backends.
type stack struct {
	t         *testing.T
	backends  *fakes.Backends
	documents *fakes.DocumentInstances
	idemStore *fakes.IdempotencyStore
	bus       *fakes.RevocationBus
	registry  *app.Registry
	revoke    *app.Revocations
	ws        *wsapi.Handlers
	server    *Server
	http      *httptest.Server
	url       string // base URL requests go to; the httptest server unless a test serves elsewhere
	clock     *fakeClock
	signer    *auth.Signer
	logs      *syncBuffer
}

func generousLimits() domain.Limits {
	limits := domain.Limits{}
	for _, b := range domain.Budgets {
		for _, s := range []domain.SubjectKind{domain.SubjectUser, domain.SubjectIP} {
			limits[domain.BudgetKey{Budget: b, Subject: s}] = ratelimit.PerSecond(100000, 100000)
		}
	}
	return limits
}

func newStack(t *testing.T, opts stackOptions) *stack {
	t.Helper()
	if opts.limits == nil {
		opts.limits = generousLimits()
	}
	if len(opts.instances) == 0 {
		opts.instances = []string{"documents-a:9090", "documents-b:9090", "documents-c:9090"}
	}
	s := &stack{t: t, clock: &fakeClock{now: time.Unix(1_700_000_000, 0)}, logs: &syncBuffer{}}
	logger := slog.New(slog.NewJSONHandler(s.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if opts.limiter == nil {
		opts.limiter = ratelimit.NewTokenBucket(s.clock)
	}
	key, err := auth.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	s.signer = auth.NewSigner(key, time.Now)
	verifier := auth.NewVerifier(auth.NewKeySet(s.signer.PublicKey()), auth.VerifierOptions{Leeway: time.Nanosecond})

	s.backends = fakes.StartBackends(t)
	s.documents = fakes.StartDocumentInstances(t, opts.instances...)
	clients := backends.NewClients(s.backends.Conns)
	throttler, err := app.NewThrottler(opts.limiter, opts.limits, logger, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.idemStore = fakes.NewIdempotencyStore(nil)
	idem, err := app.NewIdempotency(s.idemStore, app.IdempotencyOptions{Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	s.registry = app.NewRegistry()
	s.bus = fakes.NewRevocationBus()
	s.revoke = app.NewRevocations(s.bus, s.registry, logger)
	routes, err := LoadRoutes()
	if err != nil {
		t.Fatal(err)
	}
	clientIP := edge.ClientIPBehind(nil)
	ctx := context.Background()
	rest, err := NewREST(ctx, RESTConfig{Clients: clients, Routes: routes, Verifier: verifier, Throttler: throttler,
		Idempotency: idem, ClientIP: clientIP, Logger: logger, RequestTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	s.ws, err = wsapi.New(wsapi.Config{Verifier: verifier, Throttler: throttler, ClientIP: clientIP, Registry: s.registry,
		Sync: s.documents, NoteAccess: backends.NoteAccess{Identity: clients.Identity},
		Agent: clients.Agent, Notifications: clients.Notifications, Options: opts.ws, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	s.server = NewServer(ServerConfig{REST: rest, WebSocket: s.ws, Registry: s.registry, CORS: opts.cors,
		Logger: logger, DrainDelay: opts.drain})
	s.http = httptest.NewServer(s.server.Handler())
	s.url = s.http.URL
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.registry.Drain(ctx)
		_ = s.ws.StopRuns(ctx)
		s.http.Close()
		if err := s.registry.CheckInvariants(); err != nil {
			t.Error(err)
		}
		if err := s.idemStore.CheckInvariants(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func (s *stack) token(userID string, ttl time.Duration) string {
	s.t.Helper()
	token, _, err := s.signer.Issue(auth.Subject{UserID: userID}, ttl)
	if err != nil {
		s.t.Fatal(err)
	}
	return token
}

type request struct {
	method, path, body, token string
	header                    http.Header
}

func (s *stack) do(r request) *http.Response {
	s.t.Helper()
	var body io.Reader
	if r.body != "" {
		body = strings.NewReader(r.body)
	}
	req, err := http.NewRequest(r.method, s.url+r.path, body)
	if err != nil {
		s.t.Fatal(err)
	}
	for k, vs := range r.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if r.body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// decodeStatus parses a google.rpc.Status error body.
func decodeStatus(t *testing.T, resp *http.Response) *spb.Status {
	t.Helper()
	data := readBody(t, resp)
	var st spb.Status
	if err := protojson.Unmarshal(data, &st); err != nil {
		t.Fatalf("body is not a google.rpc.Status: %v: %s", err, data)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("error Content-Type = %q", ct)
	}
	return &st
}

func jsonObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not JSON: %s", data)
	}
	return m
}
