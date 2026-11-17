//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/gateway/internal/adapters/redisstore"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
	"github.com/genuinebnt/skein-go/services/gateway/internal/testredis"
)

// The REST edge with the production Redis adapters (as cmd/gateway wires
// them) instead of the in-memory ones: one request too many gets 429 with
// the documented headers, and a retried POST with the same
// Idempotency-Key is replayed from Redis without reaching the backend.
func TestRESTEdgeLimitsAndReplaysThroughRealRedis(t *testing.T) {
	client, namespace := testredis.Open(t)
	limits := generousLimits()
	limits[domain.BudgetKey{Budget: domain.BudgetAPI, Subject: domain.SubjectUser}] = ratelimit.PerSecond(1, 2)
	s := newStack(t, stackOptions{
		limits:      limits,
		limiter:     ratelimit.NewRedisGCRA(client, namespace+":rl"),
		idempotency: redisstore.NewIdempotencyStore(client, namespace),
	})
	var created atomic.Int32
	s.backends.OnUnary("/skein.v1.DocumentService/CreateNote", func(context.Context, proto.Message) (proto.Message, error) {
		created.Add(1)
		return &skeinv1.CreateNoteResponse{Note: &skeinv1.Note{Id: "note-1"}}, nil
	})
	token := s.token("ada", time.Hour)

	first := createNote(s, token, "retry-1", `{"title":"Raft"}`)
	firstBody := readBody(t, first)
	second := createNote(s, token, "retry-1", `{"title":"Raft"}`)
	if first.StatusCode != http.StatusOK || second.StatusCode != http.StatusOK ||
		second.Header.Get("Idempotent-Replayed") != "true" || string(readBody(t, second)) != string(firstBody) {
		t.Fatalf("replay: %d, %d replayed=%q", first.StatusCode, second.StatusCode, second.Header.Get("Idempotent-Replayed"))
	}
	if n := created.Load(); n != 1 {
		t.Errorf("the backend ran %d times for one idempotency key", n)
	}

	third := createNote(s, token, "retry-2", `{"title":"Paxos"}`)
	if third.StatusCode != http.StatusTooManyRequests || third.Header.Get("Retry-After") == "" || third.Header.Get("RateLimit-Remaining") != "0" {
		t.Fatalf("over budget: %d headers %v", third.StatusCode, rateLimitHeaders(third))
	}
	if n := created.Load(); n != 1 {
		t.Errorf("a limited request reached the backend")
	}
	time.Sleep(time.Second + 100*time.Millisecond)
	if resp := createNote(s, token, "retry-2", `{"title":"Paxos"}`); resp.StatusCode != http.StatusOK || resp.Header.Get("Idempotent-Replayed") != "" {
		t.Errorf("after Retry-After: %d replayed=%q", resp.StatusCode, resp.Header.Get("Idempotent-Replayed"))
	}
}
