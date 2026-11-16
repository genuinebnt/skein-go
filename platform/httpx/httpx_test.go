package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/ratelimit"
)

func TestStatusFromCodeMapsEveryCode(t *testing.T) {
	want := map[codes.Code]int{
		codes.OK: 200, codes.Canceled: 499, codes.Unknown: 500, codes.InvalidArgument: 400,
		codes.DeadlineExceeded: 504, codes.NotFound: 404, codes.AlreadyExists: 409,
		codes.PermissionDenied: 403, codes.ResourceExhausted: 429, codes.FailedPrecondition: 400,
		codes.Aborted: 409, codes.OutOfRange: 400, codes.Unimplemented: 501, codes.Internal: 500,
		codes.Unavailable: 503, codes.DataLoss: 500, codes.Unauthenticated: 401,
	}
	for c := codes.OK; c <= codes.Unauthenticated; c++ {
		if got := StatusFromCode(c); got != want[c] {
			t.Errorf("%v -> %d, want %d", c, got, want[c])
		}
		if CodeName(c) == "UNKNOWN" && c != codes.Unknown {
			t.Errorf("%v has no canonical name", c)
		}
	}
	if StatusFromCode(codes.Code(99)) != 500 || CodeName(codes.Code(99)) != "UNKNOWN" {
		t.Error("codes outside the table must map to 500 UNKNOWN")
	}
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) ErrorDetail {
	t.Helper()
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %q", rec.Body.String())
	}
	return body.Error
}

func TestWriteErrorHidesServerFaultsAndKeepsClientErrors(t *testing.T) {
	var logs bytes.Buffer
	log := logging.New(logging.Options{Writer: &logs})
	cases := []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{status.Error(codes.NotFound, "note 0193 not found"), 404, "NOT_FOUND", "note 0193 not found"},
		{status.Error(codes.Internal, "pq: relation notes does not exist"), 500, "INTERNAL", "Internal Server Error"},
		{errors.New("plain error"), 500, "UNKNOWN", "Internal Server Error"},
		{fmt.Errorf("call: %w", context.Canceled), 499, "CANCELLED", "call: context canceled"},
		{context.DeadlineExceeded, 504, "DEADLINE_EXCEEDED", "Gateway Timeout"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/v1/notes", nil)
		r = r.WithContext(logging.WithRequestID(r.Context(), "req-7"))
		WriteError(rec, r, log, c.err)
		got := decodeError(t, rec)
		if rec.Code != c.status || got.Code != c.code || got.Message != c.message || got.RequestID != "req-7" {
			t.Errorf("%v: got %d %+v, want %d %s %q", c.err, rec.Code, got, c.status, c.code, c.message)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("content type %q", ct)
		}
	}
	if !strings.Contains(logs.String(), "pq: relation notes does not exist") {
		t.Error("the hidden 5xx message must still be logged")
	}
}

func TestWriteErrorTurnsRetryInfoIntoRetryAfter(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest(http.MethodGet, "/", nil), nil, grpcx.ResourceExhausted(1500*time.Millisecond))
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("got %d Retry-After %q, want 429 and 2 (rounded up)", rec.Code, rec.Header().Get("Retry-After"))
	}
}

type chainFixture struct {
	handler http.Handler
	logs    *bytes.Buffer
	spans   *tracetest.InMemoryExporter
	metrics *sdkmetric.ManualReader
}

func newChain(cors CORSConfig) *chainFixture {
	f := &chainFixture{logs: &bytes.Buffer{}, spans: tracetest.NewInMemoryExporter(), metrics: sdkmetric.NewManualReader()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/notes/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"request_id":%q}`, logging.RequestID(r.Context()))
	})
	mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	f.handler = Chain(Config{
		Operation:      "gateway",
		Logger:         logging.New(logging.Options{Service: "gateway", Writer: f.logs}),
		CORS:           cors,
		HSTS:           true,
		TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSyncer(f.spans)),
		MeterProvider:  sdkmetric.NewMeterProvider(sdkmetric.WithReader(f.metrics)),
	}, mux)
	return f
}

func (f *chainFixture) do(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, r)
	return rec
}

func TestChainSetsRequestIDLogsRouteAndNeverTheQuery(t *testing.T) {
	f := newChain(CORSConfig{})
	rec := f.do(httptest.NewRequest(http.MethodGet, "/v1/notes/0193?share_token=s3cret", nil))
	id := rec.Header().Get(RequestIDHeader)
	if rec.Code != 200 || !logging.ValidRequestID(id) || !strings.Contains(rec.Body.String(), id) {
		t.Fatalf("status %d id %q body %s", rec.Code, id, rec.Body.String())
	}
	logs := f.logs.String()
	for _, want := range []string{`"http.route":"GET /v1/notes/{id}"`, `"status":200`, `"request_id":"` + id + `"`, `"trace_id"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("access log missing %s: %s", want, logs)
		}
	}
	if strings.Contains(logs, "s3cret") || strings.Contains(logs, "/v1/notes/0193") {
		t.Errorf("access log contains the raw URL: %s", logs)
	}

	r := httptest.NewRequest(http.MethodGet, "/v1/notes/1", nil)
	r.Header.Set(RequestIDHeader, "from-client-1")
	if got := f.do(r).Header().Get(RequestIDHeader); got != "from-client-1" {
		t.Errorf("valid client request id replaced by %q", got)
	}
}

func TestChainRecordsSpanAndDurationMetric(t *testing.T) {
	f := newChain(CORSConfig{})
	f.do(httptest.NewRequest(http.MethodGet, "/v1/notes/1", nil))
	if len(f.spans.GetSpans()) != 1 {
		t.Fatalf("want one server span, got %d", len(f.spans.GetSpans()))
	}
	var rm metricdata.ResourceMetrics
	if err := f.metrics.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			found = found || m.Name == "http.server.request.duration"
		}
	}
	if !found {
		t.Fatalf("http.server.request.duration not recorded: %+v", rm.ScopeMetrics)
	}
}

func TestPanicBecomesJSON500WithStackInLog(t *testing.T) {
	f := newChain(CORSConfig{})
	rec := f.do(httptest.NewRequest(http.MethodGet, "/panic", nil))
	got := decodeError(t, rec)
	if rec.Code != 500 || got.Code != "INTERNAL" || got.RequestID == "" || strings.Contains(rec.Body.String(), "kaboom") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(f.logs.String(), `"panic":"kaboom"`) || !strings.Contains(f.logs.String(), `"stack":"goroutine`) {
		t.Errorf("panic not logged with stack: %s", f.logs.String())
	}
	if spans := f.spans.GetSpans(); len(spans) != 1 || spans[0].Status.Code.String() != "Error" {
		t.Errorf("span not marked as error: %+v", spans)
	}
}

func TestSecurityHeadersAreSet(t *testing.T) {
	rec := newChain(CORSConfig{}).do(httptest.NewRequest(http.MethodGet, "/v1/notes/1", nil))
	for k, v := range map[string]string{
		"X-Content-Type-Options":    "nosniff",
		"X-Frame-Options":           "DENY",
		"Referrer-Policy":           "no-referrer",
		"Content-Security-Policy":   "default-src 'none'; frame-ancestors 'none'",
		"Strict-Transport-Security": "max-age=63072000; includeSubDomains",
	} {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestCORSAllowsOnlyListedOrigins(t *testing.T) {
	f := newChain(CORSConfig{AllowedOrigins: []string{"https://app.skein.dev"}, AllowCredentials: true})

	r := httptest.NewRequest(http.MethodGet, "/v1/notes/1", nil)
	r.Header.Set("Origin", "https://app.skein.dev")
	rec := f.do(r)
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.skein.dev" ||
		rec.Header().Get("Access-Control-Allow-Credentials") != "true" ||
		!strings.Contains(rec.Header().Get("Access-Control-Expose-Headers"), "RateLimit-Remaining") {
		t.Errorf("allowed origin: %v", rec.Header())
	}

	r = httptest.NewRequest(http.MethodGet, "/v1/notes/1", nil)
	r.Header.Set("Origin", "https://evil.example")
	if rec = f.do(r); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("disallowed origin got CORS headers: %v", rec.Header())
	}
	if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Origin") {
		t.Error("missing Vary: Origin")
	}

	pre := httptest.NewRequest(http.MethodOptions, "/v1/notes/1", nil)
	pre.Header.Set("Origin", "https://app.skein.dev")
	pre.Header.Set("Access-Control-Request-Method", "DELETE")
	if rec = f.do(pre); rec.Code != 204 || !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "DELETE") ||
		rec.Header().Get("Access-Control-Max-Age") != "600" {
		t.Errorf("preflight: %d %v", rec.Code, rec.Header())
	}
	pre.Header.Set("Access-Control-Request-Method", "TRACE")
	if rec = f.do(pre); rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight for a disallowed method was approved: %v", rec.Header())
	}
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func TestRateLimitSetsHeadersAndDenies(t *testing.T) {
	limiter := ratelimit.NewTokenBucket(&fakeClock{now: time.Unix(0, 0)})
	h := RateLimit(limiter, ratelimit.PerMinute(60, 2), BySubjectOrPeer, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	do := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec
	}
	first := do()
	if first.Code != 204 || first.Header().Get("RateLimit-Limit") != "2" || first.Header().Get("RateLimit-Remaining") != "1" ||
		first.Header().Get("RateLimit-Reset") != "1" || first.Header().Get("Retry-After") != "" {
		t.Fatalf("first: %d %v", first.Code, first.Header())
	}
	do()
	denied := do()
	if denied.Code != 429 || denied.Header().Get("Retry-After") != "1" || denied.Header().Get("RateLimit-Remaining") != "0" {
		t.Fatalf("denied: %d %v", denied.Code, denied.Header())
	}
	if decodeError(t, denied).Code != "RESOURCE_EXHAUSTED" {
		t.Errorf("body %s", denied.Body.String())
	}
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string, ratelimit.Limit, int64) (ratelimit.Decision, error) {
	return ratelimit.Decision{}, errors.New("redis down")
}

func TestRateLimiterFailureFailsOpen(t *testing.T) {
	var logs bytes.Buffer
	h := RateLimit(failingLimiter{}, ratelimit.PerSecond(1, 1), BySubjectOrPeer, slog.New(slog.NewJSONHandler(&logs, nil)))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 204 || !strings.Contains(logs.String(), "rate limiter failed") {
		t.Fatalf("got %d, logs %s", rec.Code, logs.String())
	}
}

func TestAuthenticateRequiresAValidBearerToken(t *testing.T) {
	key, _ := auth.GenerateKey()
	signer := auth.NewSigner(key, nil)
	verifier := auth.NewVerifier(auth.NewKeySet(signer.PublicKey()), auth.VerifierOptions{})
	h := Authenticate(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, _ := auth.SubjectFromContext(r.Context())
		_, _ = w.Write([]byte(s.UserID))
	}))
	token, _, _ := signer.Issue(auth.Subject{UserID: "u1"}, time.Minute)

	for name, header := range map[string]string{"missing": "", "basic": "Basic dTpw", "garbage": "Bearer nope"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: got %d %v", name, rec.Code, rec.Header())
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 || rec.Body.String() != "u1" {
		t.Fatalf("valid token: %d %q", rec.Code, rec.Body.String())
	}
}
