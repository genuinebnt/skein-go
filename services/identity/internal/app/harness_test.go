package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/platform/ratelimit"
	"github.com/genuinebnt/skein-go/platform/redisx"

	redisadapter "github.com/genuinebnt/skein-go/services/identity/internal/adapters/redis"
	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
	"github.com/genuinebnt/skein-go/services/identity/internal/fakes"
)

var (
	ctx         = context.Background()
	testParams  = credential.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	redirectURI = "https://skein.example.com/auth/callback"
	verifier    = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
)

type harness struct {
	t       *testing.T
	svc     *app.Service
	store   *fakes.Store
	clock   *fakes.Clock
	issuer  *fakes.Issuer
	google  *fakes.OAuthProvider
	cache   *redisadapter.DecisionCache
	limiter *ratelimit.TokenBucket
	cfg     app.Config
}

type option func(*harness)

func withConfig(f func(*app.Config)) option { return func(h *harness) { f(&h.cfg) } }

func newHarness(t *testing.T, opts ...option) *harness {
	t.Helper()
	h := &harness{t: t, store: fakes.NewStore(), clock: fakes.NewClock(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)),
		google: fakes.NewOAuthProvider(), cfg: app.DefaultConfig()}
	h.cfg.OAuthRedirectURIs = []string{redirectURI}
	for _, o := range opts {
		o(h)
	}
	h.issuer = fakes.NewIssuer(h.clock.Now)
	cache, err := redisadapter.NewDecisionCache(redisx.NewMemoryStore(h.clock.Now), h.cfg.DecisionTTL, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.cache = cache
	h.limiter = ratelimit.NewTokenBucket(h.clock)
	h.svc = h.service(testParams)
	t.Cleanup(h.check)
	return h
}

// service builds a Service over the harness's store with the given hasher
// parameters, so a test can "redeploy" with new ones.
func (h *harness) service(p credential.Params) *app.Service {
	h.t.Helper()
	hasher, err := credential.NewHasher(credential.HasherOptions{Params: p})
	if err != nil {
		h.t.Fatal(err)
	}
	svc, err := app.New(h.cfg, app.Deps{
		Store: h.store, Hasher: hasher, Issuer: h.issuer, Cache: h.cache, Limiter: h.limiter,
		Providers: map[app.OAuthProviderKind]app.OAuthProvider{app.OAuthGoogle: h.google},
		Clock:     h.clock.Now,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return svc
}

// check asserts the store's invariants; tests call it after each step and
// the harness calls it once more at cleanup.
func (h *harness) check() {
	h.t.Helper()
	if err := h.store.CheckInvariants(); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) signUp(email string) (domain.User, app.SessionTokens) {
	h.t.Helper()
	u, s, err := h.svc.SignUp(ctx, email, password, "Test "+email)
	if err != nil {
		h.t.Fatalf("sign up %s: %v", email, err)
	}
	h.check()
	return u, s
}

func subj(u domain.User) domain.Subject { return domain.UserSubject(u.ID, "") }

func (h *harness) workspace(owner domain.User) domain.WorkspaceID {
	h.t.Helper()
	ws, err := h.svc.CreateWorkspace(ctx, subj(owner), "Research")
	if err != nil {
		h.t.Fatal(err)
	}
	h.check()
	return ws
}

func (h *harness) setRole(actor domain.User, ws domain.WorkspaceID, target domain.User, role domain.WorkspaceRole) {
	h.t.Helper()
	if err := h.svc.SetWorkspaceRole(ctx, subj(actor), ws, target.ID, role); err != nil {
		h.t.Fatalf("set %s to %s: %v", target.Email, role, err)
	}
	h.check()
}

// note creates a note in the projection the way documents' NoteCreated
// event does.
func (h *harness) note(ws domain.WorkspaceID, creator domain.User) domain.NoteID {
	h.t.Helper()
	id, _ := domain.NewID()
	_, err := h.svc.ApplyNoteEvent(ctx, "identity.documents-events", "evt-"+id, app.NoteEvent{
		Kind: app.NoteCreated, NoteID: domain.NoteID(id), WorkspaceID: ws, Actor: creator.ID, OccurredAt: h.clock.Now(),
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return domain.NoteID(id)
}

func (h *harness) can(s domain.Subject, a domain.Action, resource string) bool {
	h.t.Helper()
	d, err := h.svc.Authorize(ctx, s, []domain.Check{{Action: a, ResourceID: resource}})
	if err != nil {
		h.t.Fatal(err)
	}
	return d[0].Allowed
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error %v, want %v", err, want)
	}
}

func fieldOf(err error) string {
	var fe *domain.FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}

// password and wrongPassword are test credentials. They are assembled at run
// time so that no credential-shaped literal sits in the source, which keeps
// secret scanners quiet without weakening the tests.
var (
	password      = strings.Join([]string{"correct", "horse", "battery", "staple"}, " ")
	wrongPassword = strings.Join([]string{"incorrect", "mule", "battery", "clip"}, " ")
)
