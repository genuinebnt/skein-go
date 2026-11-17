package admin_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"

	"github.com/genuinebnt/skein-go/services/identity/internal/adapters/admin"
	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

type noKeys struct{}

func (noKeys) JWKS() auth.JWKS { return auth.JWKS{} }

type fakeDevSessions struct{ users map[string]domain.User }

func (f fakeDevSessions) StartDevSession(_ context.Context, userID string) (domain.User, app.SessionTokens, error) {
	u, ok := f.users[userID]
	if !ok {
		return domain.User{}, app.SessionTokens{}, domain.ErrNotFound
	}
	return u, app.SessionTokens{AccessToken: "access-" + userID, RefreshToken: "refresh-" + userID,
		AccessExpiresAt: time.Unix(100, 0), RefreshExpiresAt: time.Unix(200, 0)}, nil
}

func post(t *testing.T, h http.Handler, body string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, admin.DevSessionPath, strings.NewReader(body)))
	return rec.Result()
}

func TestDevSessionRouteDoesNotExistUnlessEnabled(t *testing.T) {
	resp := post(t, admin.Handler(noKeys{}, nil, nil, nil), `{"userId":"u1"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
}

func TestDevSessionRouteAnswersLikeSignIn(t *testing.T) {
	h := admin.Handler(noKeys{}, nil, nil, fakeDevSessions{users: map[string]domain.User{
		"u1": {ID: "u1", Email: "ada@seed.example", DisplayName: "Ada"},
	}})
	resp := post(t, h, `{"userId":"u1"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	data, _ := io.ReadAll(resp.Body)
	var got skeinv1.SignInResponse
	if err := protojson.Unmarshal(data, &got); err != nil {
		t.Fatalf("not a SignInResponse: %v: %s", err, data)
	}
	if got.GetUser().GetId() != "u1" || got.GetSession().GetAccessToken() != "access-u1" || got.GetSession().GetRefreshToken() != "refresh-u1" {
		t.Errorf("response %v", &got)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control %q", cc)
	}
	if resp := post(t, h, `{"userId":"nobody"}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown user: status %d", resp.StatusCode)
	}
	if resp := post(t, h, `{}`); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing user: status %d", resp.StatusCode)
	}
}
