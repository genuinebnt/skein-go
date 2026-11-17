package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/api/visibility"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/httpx"
	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

var pathVariable = regexp.MustCompile(`\{[^}]+\}`)

func routePath(template string) string { return pathVariable.ReplaceAllString(template, "id1") }

func routeBody(r Route) string {
	if r.Verb == http.MethodGet || r.Verb == http.MethodDelete {
		return ""
	}
	return "{}"
}

func detail[T proto.Message](t *testing.T, st *spb.Status, into T) (T, bool) {
	t.Helper()
	for _, d := range st.GetDetails() {
		if d.MessageIs(into) {
			if err := d.UnmarshalTo(into); err != nil {
				t.Fatal(err)
			}
			return into, true
		}
	}
	return into, false
}

// Every annotated RPC is reachable at its documented route, and the
// backend receives the verified token, the user ID and the request ID.
func TestEveryDocumentedRESTRouteReachesItsRPCWithTheCallersIdentity(t *testing.T) {
	s := newStack(t, stackOptions{})
	routes, _ := LoadRoutes()
	token := s.token("user-1", time.Hour)
	for _, r := range routes.Routes() {
		resp := s.do(request{method: r.Verb, path: routePath(r.Template), body: routeBody(r), token: token})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s: status %d %s", r.Verb, r.Template, resp.StatusCode, readBody(t, resp))
			continue
		}
		calls := s.backends.CallsTo(r.Method)
		if len(calls) != 1 {
			t.Errorf("%s %s reached %s %d times", r.Verb, r.Template, r.Method, len(calls))
			continue
		}
		md := calls[0].MD
		if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer "+token {
			t.Errorf("%s: authorization metadata %v", r.Method, got)
		}
		if got := md.Get("x-skein-user-id"); len(got) != 1 || got[0] != "user-1" {
			t.Errorf("%s: x-skein-user-id %v", r.Method, got)
		}
		if got := md.Get("x-request-id"); len(got) != 1 || got[0] != resp.Header.Get("X-Request-Id") {
			t.Errorf("%s: x-request-id %v, response header %q", r.Method, got, resp.Header.Get("X-Request-Id"))
		}
	}
	// The route table, the OpenAPI document and the generated handlers
	// describe the same set of operations.
	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openAPIDocument, &doc); err != nil {
		t.Fatal(err)
	}
	operations := 0
	for _, methods := range doc.Paths {
		operations += len(methods)
	}
	if operations != len(routes.Routes()) || operations < 40 {
		t.Errorf("OpenAPI has %d operations, route table %d", operations, len(routes.Routes()))
	}
}

func TestPathAndQueryParametersBindToTheRequest(t *testing.T) {
	s := newStack(t, stackOptions{})
	resp := s.do(request{method: "GET", path: "/v1/notes/n-42?includeMarkdown=true", token: s.token("u", time.Hour)})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	got := s.backends.CallsTo("/skein.v1.DocumentService/GetNote")[0].Request.(*skeinv1.GetNoteRequest)
	if got.GetNoteId() != "n-42" || !got.GetIncludeMarkdown() {
		t.Errorf("request %v", got)
	}
}

func TestProtectedRoutesRefuseMissingInvalidAndExpiredTokens(t *testing.T) {
	s := newStack(t, stackOptions{})
	expired := s.token("u", time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	cases := []struct {
		name, token, reason string
	}{
		{"missing", "", "TOKEN_INVALID"},
		{"garbage", "not-a-token", "TOKEN_INVALID"},
		{"tampered", s.token("u", time.Hour) + "x", "TOKEN_INVALID"},
		{"expired", expired, "TOKEN_EXPIRED"},
	}
	for _, c := range cases {
		resp := s.do(request{method: "GET", path: "/v1/notes/n1", token: c.token})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d", c.name, resp.StatusCode)
			continue
		}
		if !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Bearer") {
			t.Errorf("%s: WWW-Authenticate %q", c.name, resp.Header.Get("WWW-Authenticate"))
		}
		st := decodeStatus(t, resp)
		info, ok := detail(t, st, &errdetails.ErrorInfo{})
		if st.GetCode() != int32(codes.Unauthenticated) || !ok || info.GetReason() != c.reason {
			t.Errorf("%s: %v", c.name, st)
		}
	}
	if n := len(s.backends.CallsTo("/skein.v1.DocumentService/GetNote")); n != 0 {
		t.Errorf("backend called %d times without a valid token", n)
	}
}

func TestSessionRoutesArePublicAndDropInvalidTokens(t *testing.T) {
	s := newStack(t, stackOptions{})
	public := map[string]string{
		"/v1/users": domain.MethodSignUp, "/v1/sessions": domain.MethodSignIn, "/v1/sessions:oauth": domain.MethodSignInWithOAuth,
		"/v1/sessions:refresh": domain.MethodRefresh, "/v1/sessions:signOut": domain.MethodSignOut,
	}
	for path, method := range public {
		for _, token := range []string{"", "invalid-token"} {
			resp := s.do(request{method: "POST", path: path, body: "{}", token: token})
			if resp.StatusCode != http.StatusOK {
				t.Errorf("POST %s with token %q: %d", path, token, resp.StatusCode)
			}
		}
		for _, c := range s.backends.CallsTo(method) {
			if len(c.MD.Get("authorization")) != 0 || len(c.MD.Get("x-skein-user-id")) != 0 {
				t.Errorf("%s: unverified identity forwarded: %v", method, c.MD)
			}
		}
	}
	// A valid token on a public route is still honoured (sign out uses it).
	s.do(request{method: "POST", path: "/v1/sessions:signOut", body: "{}", token: s.token("u9", time.Hour)})
	calls := s.backends.CallsTo(domain.MethodSignOut)
	if got := calls[len(calls)-1].MD.Get("x-skein-user-id"); len(got) != 1 || got[0] != "u9" {
		t.Errorf("valid token on a public route not forwarded: %v", got)
	}
}

func TestClientsCannotForgeTheForwardedIdentity(t *testing.T) {
	s := newStack(t, stackOptions{})
	header := http.Header{}
	header.Set("Grpc-Metadata-X-Skein-User-Id", "mallory")
	header.Set("X-Skein-User-Id", "mallory")
	header.Set("Grpc-Metadata-Authorization", "Bearer forged")
	s.do(request{method: "GET", path: "/v1/notes/n1", token: s.token("alice", time.Hour), header: header})
	s.do(request{method: "POST", path: "/v1/sessions", body: "{}", header: header})
	for _, c := range s.backends.Calls() {
		users, auths := c.MD.Get("x-skein-user-id"), c.MD.Get("authorization")
		if len(users) > 1 || (len(users) == 1 && users[0] != "alice") || len(auths) > 1 || (len(auths) == 1 && auths[0] == "Bearer forged") {
			t.Errorf("%s got forged metadata: %v", c.Method, c.MD)
		}
	}
}

// The table in docs/api/README.md, with the body rules: RequestInfo
// always, generic messages for server faults, RetryInfo for UNAVAILABLE,
// and backend details passed through for client errors.
func TestBackendErrorsFollowTheDocumentedMapping(t *testing.T) {
	s := newStack(t, stackOptions{})
	token := s.token("u", time.Hour)
	table := map[codes.Code]int{
		codes.InvalidArgument: 400, codes.FailedPrecondition: 400, codes.OutOfRange: 400,
		codes.Unauthenticated: 401, codes.PermissionDenied: 403, codes.NotFound: 404,
		codes.AlreadyExists: 409, codes.Aborted: 409, codes.ResourceExhausted: 429,
		codes.Canceled: 499, codes.Internal: 500, codes.Unknown: 500, codes.DataLoss: 500,
		codes.Unimplemented: 501, codes.Unavailable: 503, codes.DeadlineExceeded: 504,
	}
	for code, httpStatus := range table {
		s.backends.OnUnary("/skein.v1.DocumentService/GetNote", func(context.Context, proto.Message) (proto.Message, error) {
			st, _ := status.New(code, "db host 10.0.0.7 said no").WithDetails(&errdetails.DebugInfo{Detail: "stack"},
				&errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: "noteId"}}})
			return nil, st.Err()
		})
		resp := s.do(request{method: "GET", path: "/v1/notes/n1", token: token})
		if resp.StatusCode != httpStatus {
			t.Errorf("%s: HTTP %d, want %d", code, resp.StatusCode, httpStatus)
		}
		st := decodeStatus(t, resp)
		if st.GetCode() != int32(code) {
			t.Errorf("%s: body code %d", code, st.GetCode())
		}
		info, ok := detail(t, st, &errdetails.RequestInfo{})
		if !ok || info.GetRequestId() != resp.Header.Get("X-Request-Id") {
			t.Errorf("%s: RequestInfo %v (header %q)", code, info, resp.Header.Get("X-Request-Id"))
		}
		_, hasDebug := detail(t, st, &errdetails.DebugInfo{})
		_, hasBadRequest := detail(t, st, &errdetails.BadRequest{})
		if httpStatus >= 500 {
			if strings.Contains(st.GetMessage(), "10.0.0.7") || hasDebug || hasBadRequest {
				t.Errorf("%s: server fault leaked details: %v", code, st)
			}
		} else if !strings.Contains(st.GetMessage(), "said no") || !hasBadRequest {
			t.Errorf("%s: client error lost its message or details: %v", code, st)
		}
		if code == codes.Unavailable {
			if _, ok := detail(t, st, &errdetails.RetryInfo{}); !ok || resp.Header.Get("Retry-After") != "1" {
				t.Errorf("UNAVAILABLE without RetryInfo/Retry-After: %v %q", st, resp.Header.Get("Retry-After"))
			}
		}
	}
}

func TestUnknownPathsAndInternalRPCsAreNotFound(t *testing.T) {
	s := newStack(t, stackOptions{})
	token := s.token("u", time.Hour)
	for _, path := range []string{"/v1/authorize", "/skein.v1.IdentityService/Authorize", "/v1/delegated-tokens", "/nope"} {
		resp := s.do(request{method: "POST", path: path, body: "{}", token: token})
		if resp.StatusCode != http.StatusNotFound || decodeStatus(t, resp).GetCode() != int32(codes.NotFound) {
			t.Errorf("POST %s: %d", path, resp.StatusCode)
		}
	}
	if n := len(s.backends.CallsTo("/skein.v1.IdentityService/Authorize")); n != 0 {
		t.Errorf("internal RPC reached %d times", n)
	}
}

func TestRequestIDIsKeptWhenValidAndGeneratedOtherwise(t *testing.T) {
	s := newStack(t, stackOptions{})
	header := http.Header{}
	header.Set("X-Request-Id", "client-chosen.id_1")
	resp := s.do(request{method: "GET", path: "/v1/notes/n1", token: s.token("u", time.Hour), header: header})
	if got := resp.Header.Get("X-Request-Id"); got != "client-chosen.id_1" {
		t.Errorf("valid ID replaced: %q", got)
	}
	header.Set("X-Request-Id", "bad id with spaces")
	resp = s.do(request{method: "GET", path: "/v1/notes/n1", token: s.token("u", time.Hour), header: header})
	if got := resp.Header.Get("X-Request-Id"); got == "" || strings.Contains(got, " ") {
		t.Errorf("invalid ID kept: %q", got)
	}
	for _, h := range []string{"X-Content-Type-Options", "Content-Security-Policy", "X-Frame-Options"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("security header %s missing", h)
		}
	}
}

func rateLimitHeaders(resp *http.Response) [4]string {
	return [4]string{resp.Header.Get("RateLimit-Limit"), resp.Header.Get("RateLimit-Remaining"),
		resp.Header.Get("RateLimit-Reset"), resp.Header.Get("Retry-After")}
}

func TestRateLimitHeadersOnEveryResponseAnd429OverBudget(t *testing.T) {
	limits := generousLimits()
	limits[domain.BudgetKey{Budget: domain.BudgetAPI, Subject: domain.SubjectUser}] = ratelimit.PerMinute(60, 2)
	limits[domain.BudgetKey{Budget: domain.BudgetSearch, Subject: domain.SubjectUser}] = ratelimit.PerMinute(60, 1)
	s := newStack(t, stackOptions{limits: limits})
	token := s.token("u", time.Hour)

	first := s.do(request{method: "GET", path: "/v1/notes/n1", token: token})
	if got := rateLimitHeaders(first); got != [4]string{"2", "1", "1", ""} {
		t.Errorf("first: headers %v", got)
	}
	s.do(request{method: "GET", path: "/v1/notes/n1", token: token})
	third := s.do(request{method: "GET", path: "/v1/notes/n1", token: token})
	if third.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third: %d", third.StatusCode)
	}
	if got := rateLimitHeaders(third); got != [4]string{"2", "0", "2", "1"} {
		t.Errorf("429 headers %v", got)
	}
	st := decodeStatus(t, third)
	quota, okQuota := detail(t, st, &errdetails.QuotaFailure{})
	retry, okRetry := detail(t, st, &errdetails.RetryInfo{})
	if st.GetCode() != int32(codes.ResourceExhausted) || !okQuota || quota.GetViolations()[0].GetSubject() != "api:user" ||
		!okRetry || retry.GetRetryDelay().AsDuration() != time.Second {
		t.Errorf("429 body %v", st)
	}
	if n := len(s.backends.CallsTo("/skein.v1.DocumentService/GetNote")); n != 2 {
		t.Errorf("backend called %d times, want 2", n)
	}
	// Search has its own budget, untouched by the api requests.
	if resp := s.do(request{method: "GET", path: "/v1/workspaces/w/search?query=x", token: token}); resp.StatusCode != http.StatusOK {
		t.Errorf("search budget shared with api: %d", resp.StatusCode)
	}
	if resp := s.do(request{method: "GET", path: "/v1/workspaces/w/search?query=x", token: token}); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second search: %d", resp.StatusCode)
	}
	// After waiting Retry-After, the request goes through.
	s.clock.Advance(time.Second)
	if resp := s.do(request{method: "GET", path: "/v1/notes/n1", token: token}); resp.StatusCode != http.StatusOK {
		t.Errorf("after Retry-After: %d", resp.StatusCode)
	}
}

func TestAnonymousRequestsAreLimitedPerIP(t *testing.T) {
	limits := generousLimits()
	limits[domain.BudgetKey{Budget: domain.BudgetAPI, Subject: domain.SubjectIP}] = ratelimit.PerMinute(60, 1)
	s := newStack(t, stackOptions{limits: limits})
	s.do(request{method: "POST", path: "/v1/sessions", body: "{}"})
	resp := s.do(request{method: "POST", path: "/v1/sessions", body: "{}"})
	st := decodeStatus(t, resp)
	quota, _ := detail(t, st, &errdetails.QuotaFailure{})
	if resp.StatusCode != http.StatusTooManyRequests || quota.GetViolations()[0].GetSubject() != "api:ip" {
		t.Errorf("anonymous flood: %d %v", resp.StatusCode, st)
	}
	if !strings.Contains(s.logs.String(), `"client_ip":"127.0.0.1"`) {
		t.Error("the WARN line for an IP denial must name the IP")
	}
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string, ratelimit.Limit, int64) (ratelimit.Decision, error) {
	return ratelimit.Decision{}, errors.New("redis: connection refused")
}

func TestRateLimiterOutageFailsOpen(t *testing.T) {
	s := newStack(t, stackOptions{limiter: failingLimiter{}})
	resp := s.do(request{method: "GET", path: "/v1/notes/n1", token: s.token("u", time.Hour)})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("RateLimit-Limit") != "" {
		t.Errorf("status %d, headers %v", resp.StatusCode, rateLimitHeaders(resp))
	}
	if !strings.Contains(s.logs.String(), "rate limiter failed; allowing request") {
		t.Error("limiter failure not logged")
	}
}

func createNote(s *stack, token, key, body string) *http.Response {
	header := http.Header{}
	if key != "" {
		header.Set("Idempotency-Key", key)
	}
	return s.do(request{method: "POST", path: "/v1/workspaces/w1/notes", body: body, token: token, header: header})
}

func TestIdempotencyKeyReplaysTheFirstResponse(t *testing.T) {
	s := newStack(t, stackOptions{})
	var mu sync.Mutex
	created := 0
	s.backends.OnUnary("/skein.v1.DocumentService/CreateNote", func(context.Context, proto.Message) (proto.Message, error) {
		mu.Lock()
		defer mu.Unlock()
		created++
		return &skeinv1.CreateNoteResponse{Note: &skeinv1.Note{Id: "note-" + string(rune('0'+created))}}, nil
	})
	alice, bob := s.token("alice", time.Hour), s.token("bob", time.Hour)
	first := createNote(s, alice, "k-1", `{"title":"Raft"}`)
	firstBody := readBody(t, first)
	second := createNote(s, alice, "k-1", `{"title":"Raft"}`)
	secondBody := readBody(t, second)
	if first.StatusCode != 200 || second.StatusCode != 200 || string(firstBody) != string(secondBody) {
		t.Fatalf("replay differs: %d %s / %d %s", first.StatusCode, firstBody, second.StatusCode, secondBody)
	}
	if first.Header.Get("Idempotent-Replayed") != "" || second.Header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("Idempotent-Replayed: first %q, second %q", first.Header.Get("Idempotent-Replayed"), second.Header.Get("Idempotent-Replayed"))
	}
	if created != 1 {
		t.Errorf("backend ran %d times", created)
	}
	// Keys are per caller: bob's k-1 is a new request.
	if body := readBody(t, createNote(s, bob, "k-1", `{"title":"Raft"}`)); string(body) == string(firstBody) || created != 2 {
		t.Errorf("bob got alice's response: %s", body)
	}
	// Without a key nothing is deduplicated.
	createNote(s, alice, "", `{"title":"Raft"}`)
	if created != 3 {
		t.Errorf("request without a key was deduplicated")
	}
}

func TestIdempotencyKeyReusedWithAnotherBodyIsInvalidArgument(t *testing.T) {
	s := newStack(t, stackOptions{})
	token := s.token("alice", time.Hour)
	createNote(s, token, "k-2", `{"title":"A"}`)
	resp := createNote(s, token, "k-2", `{"title":"B"}`)
	st := decodeStatus(t, resp)
	bad, ok := detail(t, st, &errdetails.BadRequest{})
	if resp.StatusCode != http.StatusBadRequest || st.GetCode() != int32(codes.InvalidArgument) || !ok ||
		bad.GetFieldViolations()[0].GetField() != "Idempotency-Key" {
		t.Errorf("conflict: %d %v", resp.StatusCode, st)
	}
	// Same key on another path conflicts too.
	header := http.Header{"Idempotency-Key": {"k-2"}}
	if resp := s.do(request{method: "POST", path: "/v1/workspaces", body: `{"title":"A"}`, token: token, header: header}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("same key on another path: %d", resp.StatusCode)
	}
	if resp := createNote(s, token, strings.Repeat("k", 256), "{}"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("overlong key: %d", resp.StatusCode)
	}
}

func TestIdempotentRetryWhileTheFirstRunsIsAborted(t *testing.T) {
	s := newStack(t, stackOptions{})
	started, release := make(chan struct{}), make(chan struct{})
	s.backends.OnUnary("/skein.v1.DocumentService/CreateNote", func(context.Context, proto.Message) (proto.Message, error) {
		close(started)
		<-release
		return &skeinv1.CreateNoteResponse{}, nil
	})
	token := s.token("alice", time.Hour)
	done := make(chan int)
	go func() { done <- createNote(s, token, "k-3", "{}").StatusCode }()
	<-started
	resp := createNote(s, token, "k-3", "{}")
	st := decodeStatus(t, resp)
	if resp.StatusCode != http.StatusConflict || st.GetCode() != int32(codes.Aborted) {
		t.Errorf("concurrent retry: %d %v", resp.StatusCode, st)
	}
	close(release)
	if code := <-done; code != 200 {
		t.Errorf("first request: %d", code)
	}
}

func TestUnstorableResponsesLetTheRetryRunAgain(t *testing.T) {
	s := newStack(t, stackOptions{})
	calls := 0
	s.backends.OnUnary("/skein.v1.DocumentService/CreateNote", func(context.Context, proto.Message) (proto.Message, error) {
		calls++
		if calls == 1 {
			return nil, status.Error(codes.Unavailable, "down")
		}
		return &skeinv1.CreateNoteResponse{}, nil
	})
	token := s.token("alice", time.Hour)
	if resp := createNote(s, token, "k-4", "{}"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("first: %d", resp.StatusCode)
	}
	if resp := createNote(s, token, "k-4", "{}"); resp.StatusCode != http.StatusOK || resp.Header.Get("Idempotent-Replayed") != "" {
		t.Errorf("retry after 503: %d replayed=%q", resp.StatusCode, resp.Header.Get("Idempotent-Replayed"))
	}
	if calls != 2 {
		t.Errorf("backend ran %d times", calls)
	}
}

func TestIdempotencyKeyIsIgnoredWhereTheAPIIsIdempotentAnyway(t *testing.T) {
	s := newStack(t, stackOptions{})
	token := s.token("alice", time.Hour)
	header := http.Header{"Idempotency-Key": {"same"}}
	for range 2 {
		s.do(request{method: "POST", path: "/v1/notes/n1/ops", body: `{"ops":"AQI="}`, token: token, header: header})
		s.do(request{method: "GET", path: "/v1/notes/n1", token: token, header: header})
	}
	if a, g := len(s.backends.CallsTo(domain.MethodApplyOps)), len(s.backends.CallsTo("/skein.v1.DocumentService/GetNote")); a != 2 || g != 2 {
		t.Errorf("ApplyOps ran %d times, GetNote %d; want 2 each", a, g)
	}
	if s.idemStore.Len() != 0 {
		t.Errorf("%d records stored for exempt methods", s.idemStore.Len())
	}
}

func TestIdempotencyStoreOutageFailsOpen(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.idemStore.SetFailing(true)
	token := s.token("alice", time.Hour)
	for range 2 {
		if resp := createNote(s, token, "k-5", "{}"); resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}
	if n := len(s.backends.CallsTo("/skein.v1.DocumentService/CreateNote")); n != 2 {
		t.Errorf("backend ran %d times", n)
	}
}

func TestCORSAllowsTheDocumentedHeaders(t *testing.T) {
	cors := newStack(t, stackOptions{cors: httpx.CORSConfig{AllowedOrigins: []string{"https://app.skein.dev"}}})
	header := http.Header{}
	header.Set("Origin", "https://app.skein.dev")
	header.Set("Access-Control-Request-Method", "POST")
	resp := cors.do(request{method: "OPTIONS", path: "/v1/workspaces/w1/notes", header: header})
	if allowed := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(allowed, "Idempotency-Key") || !strings.Contains(allowed, "traceparent") {
		t.Errorf("preflight allows %q", allowed)
	}
	header.Del("Access-Control-Request-Method")
	resp = cors.do(request{method: "GET", path: "/v1/notes/n1", token: cors.token("u", time.Hour), header: header})
	// The web frontend reads each of these on cross-origin responses.
	exposed := resp.Header.Get("Access-Control-Expose-Headers")
	for _, want := range []string{"X-Request-ID", "RateLimit-Limit", "RateLimit-Remaining", "RateLimit-Reset", "Retry-After", "Idempotent-Replayed"} {
		if !strings.Contains(exposed, want) {
			t.Errorf("exposed headers %q lack %s", exposed, want)
		}
	}
}

func TestOpenAPIDocumentAndDocsPageAreServed(t *testing.T) {
	s := newStack(t, stackOptions{})
	resp := s.do(request{method: "GET", path: "/openapi.json"})
	body := readBody(t, resp)
	if resp.StatusCode != 200 || string(body) != string(openAPIDocument) {
		t.Errorf("openapi: %d", resp.StatusCode)
	}
	resp = s.do(request{method: "GET", path: "/docs"})
	page := string(readBody(t, resp))
	if resp.StatusCode != 200 || !strings.Contains(page, "/v1/notes/{noteId}:restore") || !strings.Contains(page, `href="/openapi.json"`) {
		t.Errorf("docs page: %d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); strings.Contains(csp, "script-src") || !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("docs CSP %q", csp)
	}
}

func TestEmbeddedOpenAPICopyIsCurrent(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "gen", "openapi", "skein.swagger.json"))
	if err != nil {
		t.Skipf("gen/openapi not found outside the repository: %v", err)
	}
	if string(committed) != string(openAPIDocument) {
		t.Error("openapi/skein.swagger.json is stale; run go generate ./internal/adapters/httpapi")
	}
}

// Internal RPCs carry google.api.method_visibility INTERNAL so the
// generator leaves them, and the messages only they use, out of the public
// document; AuthorizeResponse used to be listed.
func TestOpenAPIDocumentOmitsInternalRPCs(t *testing.T) {
	var doc struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal(openAPIDocument, &doc); err != nil {
		t.Fatal(err)
	}
	internal := 0
	protoregistry.GlobalFiles.RangeFilesByPackage("skein.v1", func(file protoreflect.FileDescriptor) bool {
		for i := 0; i < file.Services().Len(); i++ {
			methods := file.Services().Get(i).Methods()
			for j := 0; j < methods.Len(); j++ {
				m := methods.Get(j)
				rule, _ := proto.GetExtension(m.Options(), visibility.E_MethodVisibility).(*visibility.VisibilityRule)
				if rule.GetRestriction() != "INTERNAL" {
					continue
				}
				internal++
				if _, listed := doc.Definitions[string(m.Output().Name())]; listed {
					t.Errorf("%s is internal but %s is in the OpenAPI document", m.FullName(), m.Output().Name())
				}
			}
		}
		return true
	})
	if internal == 0 {
		t.Fatal("no INTERNAL methods found; the visibility annotations are gone")
	}
}

func TestHealthAndReadiness(t *testing.T) {
	s := newStack(t, stackOptions{})
	if resp := s.do(request{method: "GET", path: "/healthz"}); resp.StatusCode != 200 {
		t.Errorf("healthz %d", resp.StatusCode)
	}
	resp := s.do(request{method: "GET", path: "/readyz"})
	if resp.StatusCode != 200 || jsonObject(t, readBody(t, resp))["status"] != "ready" {
		t.Errorf("readyz %d", resp.StatusCode)
	}
}

func TestAccessLogShowsRouteTemplatesNotPaths(t *testing.T) {
	s := newStack(t, stackOptions{})
	s.do(request{method: "GET", path: "/v1/notes/secret-note-id?includeMarkdown=true", token: s.token("u", time.Hour)})
	logs := s.logs.String()
	if !strings.Contains(logs, `"http.route":"/v1/notes/{note_id}"`) || strings.Contains(logs, "secret-note-id") {
		t.Errorf("access log: %s", logs)
	}
}
