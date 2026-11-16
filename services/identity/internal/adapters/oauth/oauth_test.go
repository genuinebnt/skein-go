package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/genuinebnt/skein-go/platform/config"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
)

const (
	verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	redirect = "https://skein.example.com/auth/callback"
)

// provider is an in-process stand-in for a token endpoint and profile API
// that, like the real ones, insists on the verifier and redirect URI.
func provider(t *testing.T, profile map[string]any, emails []map[string]any, tokenStatus int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_id") != "client" || r.Form.Get("client_secret") != "s3cret" {
			t.Errorf("token request form %v", r.Form)
		}
		if tokenStatus != http.StatusOK {
			w.WriteHeader(tokenStatus)
			return
		}
		if r.Form.Get("code") != "good" || r.Form.Get("code_verifier") != verifier || r.Form.Get("redirect_uri") != redirect {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at-123"})
	})
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer at-123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /profile", auth(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(profile) }))
	mux.HandleFunc("GET /emails", auth(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(emails) }))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func client(srv *httptest.Server) Client {
	return Client{ID: "client", Secret: config.NewSecret("s3cret"), HTTP: srv.Client(),
		Endpoints: Endpoints{Token: srv.URL + "/token", Profile: srv.URL + "/profile", Emails: srv.URL + "/emails"}}
}

func TestGoogleExchangesWithPKCEAndReadsUserinfo(t *testing.T) {
	srv := provider(t, map[string]any{"sub": "1077", "email": "ada@example.com", "email_verified": true, "name": "Ada"}, nil, http.StatusOK)
	g := Google{Client: client(srv)}
	id, err := g.Exchange(context.Background(), "good", verifier, redirect)
	if err != nil {
		t.Fatal(err)
	}
	if id != (app.ExternalIdentity{Subject: "1077", Email: "ada@example.com", EmailVerified: true, DisplayName: "Ada"}) {
		t.Fatalf("%+v", id)
	}
	if _, err := g.Exchange(context.Background(), "good", "wrong-verifier", redirect); !errors.Is(err, app.ErrOAuthRejected) {
		t.Fatalf("wrong verifier: %v", err)
	}
	if _, err := g.Exchange(context.Background(), "good", verifier, "https://evil.example/cb"); !errors.Is(err, app.ErrOAuthRejected) {
		t.Fatalf("wrong redirect: %v", err)
	}
}

func TestGitHubUsesTheNumericIDAndThePrimaryEmail(t *testing.T) {
	srv := provider(t, map[string]any{"id": 583231, "login": "octocat", "name": ""},
		[]map[string]any{{"email": "old@example.com", "primary": false, "verified": true}, {"email": "cat@example.com", "primary": true, "verified": false}},
		http.StatusOK)
	id, err := GitHub{Client: client(srv)}.Exchange(context.Background(), "good", verifier, redirect)
	if err != nil {
		t.Fatal(err)
	}
	if id != (app.ExternalIdentity{Subject: "583231", Email: "cat@example.com", EmailVerified: false, DisplayName: "octocat"}) {
		t.Fatalf("%+v", id)
	}
}

func TestProviderFailuresMapToRejectedOrUnavailable(t *testing.T) {
	for status, want := range map[int]error{http.StatusBadRequest: app.ErrOAuthRejected, http.StatusBadGateway: app.ErrProviderUnavailable} {
		srv := provider(t, nil, nil, status)
		_, err := Google{Client: client(srv)}.Exchange(context.Background(), "good", verifier, redirect)
		if !errors.Is(err, want) {
			t.Errorf("status %d: %v, want %v", status, err, want)
		}
	}
	srv := provider(t, nil, nil, http.StatusOK)
	srv.Close()
	_, err := Google{Client: client(srv)}.Exchange(context.Background(), "good", verifier, redirect)
	if !errors.Is(err, app.ErrProviderUnavailable) {
		t.Errorf("closed server: %v", err)
	}
}
