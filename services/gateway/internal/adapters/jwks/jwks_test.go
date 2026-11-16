package jwks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/platform/auth"
)

// A rotation reaches the gateway on the first token signed with the new
// key, without waiting for the cache period: RemoteKeySet refetches on an
// unknown key ID.
func TestRotatedKeyIsFetchedOnFirstUse(t *testing.T) {
	oldKey, _ := auth.GenerateKey()
	newKey, _ := auth.GenerateKey()
	oldSigner, newSigner := auth.NewSigner(oldKey, time.Now), auth.NewSigner(newKey, time.Now)
	var published atomic.Value
	published.Store(auth.NewKeySet(oldSigner.PublicKey()).JWKS())
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		_ = json.NewEncoder(w).Encode(published.Load())
	}))
	defer srv.Close()

	now := time.Now()
	keys := auth.NewRemoteKeySet(Fetch(srv.URL, nil), auth.RemoteKeySetOptions{RefreshInterval: time.Hour, MinRefetch: time.Second,
		Now: func() time.Time { return now }})
	verifier := auth.NewVerifier(keys, auth.VerifierOptions{})
	ctx := context.Background()
	oldToken, _, _ := oldSigner.Issue(auth.Subject{UserID: "u"}, time.Minute)
	for range 3 {
		if _, err := verifier.Verify(ctx, oldToken); err != nil {
			t.Fatal(err)
		}
	}
	if fetches.Load() != 1 {
		t.Errorf("fetched %d times for a cached key", fetches.Load())
	}
	published.Store(auth.NewKeySet(oldSigner.PublicKey(), newSigner.PublicKey()).JWKS())
	now = now.Add(2 * time.Second)
	newToken, _, _ := newSigner.Issue(auth.Subject{UserID: "u"}, time.Minute)
	if _, err := verifier.Verify(ctx, newToken); err != nil {
		t.Fatalf("new key not picked up: %v", err)
	}
	if fetches.Load() != 2 {
		t.Errorf("fetched %d times", fetches.Load())
	}
}

func TestFetchReportsErrorsAndBoundsTheDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/huge":
			_, _ = w.Write([]byte(strings.Repeat("x", maxDocumentBytes+10)))
		}
	}))
	defer srv.Close()
	for _, path := range []string{"/missing", "/huge"} {
		if _, err := Fetch(srv.URL+path, srv.Client())(context.Background()); err == nil {
			t.Errorf("%s: no error", path)
		}
	}
}
