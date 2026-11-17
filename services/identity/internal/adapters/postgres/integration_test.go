//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/outbox"
	platformpg "github.com/genuinebnt/skein-go/platform/postgres"
	"github.com/genuinebnt/skein-go/platform/postgres/pgtest"

	identityevents "github.com/genuinebnt/skein-go/services/identity/internal/adapters/events"
	"github.com/genuinebnt/skein-go/services/identity/internal/adapters/postgres"
	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
	"github.com/genuinebnt/skein-go/services/identity/internal/fakes"
	"github.com/genuinebnt/skein-go/services/identity/internal/seed"
	"github.com/genuinebnt/skein-go/services/identity/internal/storetest"
	"github.com/genuinebnt/skein-go/services/identity/migrations"
)

var schemas atomic.Int64

// newDatabase starts Postgres once per test binary run of the caller and
// returns a pool on a fresh schema, migrated, as the identity role would
// see it (search_path = that schema).
func newDatabase(t *testing.T, baseURL string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	schema := fmt.Sprintf("identity_%d", schemas.Add(1))
	admin, err := pgx.Connect(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	_ = admin.Close(ctx)
	pool, err := platformpg.Open(ctx, platformpg.Config{URL: config.NewSecret(baseURL + "&search_path=" + schema)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := platformpg.Migrate(ctx, pool, migrations.FS, ".", logging.Discard()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	// SKEIN_TEST_POSTGRES_DSN runs against an existing server (make
	// up-native) instead of a container; see platform/postgres/pgtest.
	if url, ok := pgtest.FreshDatabase(t); ok {
		return url
	}
	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("skein"), tcpostgres.WithUsername("skein"), tcpostgres.WithPassword("skein"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	url, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return url
}

func TestPostgresStoreMeetsTheStoreContract(t *testing.T) {
	url := startPostgres(t)
	storetest.Run(t, func(t *testing.T) app.Store { return postgres.NewStore(newDatabase(t, url)) })
}

func TestMigrationsApplyTwiceWithoutChange(t *testing.T) {
	url := startPostgres(t)
	pool := newDatabase(t, url)
	if err := platformpg.Migrate(context.Background(), pool, migrations.FS, ".", logging.Discard()); err != nil {
		t.Fatal(err)
	}
}

// TestSeedLoadsIntoPostgresAndAuthorizeMatchesTheMatrix is the end-to-end
// check of the repositories' SQL: the small seed through the loader, then
// every permission matrix row through Authorize.
func TestSeedLoadsIntoPostgresAndAuthorizeMatchesTheMatrix(t *testing.T) {
	url := startPostgres(t)
	store := postgres.NewStore(newDatabase(t, url))
	dir := filepath.Join("..", "..", "..", "..", "..", "testdata", "seed", "small")
	rep, err := seed.Load(context.Background(), store, seed.Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Load(context.Background(), store, seed.Options{Dir: dir}); err != nil {
		t.Fatalf("second load: %v", err)
	}
	hasher, _ := credential.NewHasher(credential.HasherOptions{Params: credential.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}})
	now := func() time.Time { return rep.ReferenceTime }
	svc, err := app.New(app.DefaultConfig(), app.Deps{Store: store, Hasher: hasher, Issuer: fakes.NewIssuer(now), Clock: now})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := seed.ReadJSONL[struct {
		Subject struct {
			UserID      *string `json:"user_id"`
			AgentRunID  *string `json:"agent_run_id"`
			ShareLinkID *string `json:"share_link_id"`
		} `json:"subject"`
		Action     string `json:"action"`
		ResourceID string `json:"resource_id"`
		Expected   string `json:"expected"`
		Rule       string `json:"rule"`
	}](dir, filepath.Join("truth", "permissions", "matrix.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range rows {
		var s domain.Subject
		switch {
		case r.Subject.ShareLinkID != nil:
			s = domain.LinkSubject(domain.ShareLinkID(*r.Subject.ShareLinkID))
		case r.Subject.AgentRunID != nil:
			s = domain.UserSubject(domain.UserID(*r.Subject.UserID), domain.AgentRunID(*r.Subject.AgentRunID))
		default:
			s = domain.UserSubject(domain.UserID(*r.Subject.UserID), "")
		}
		d, err := svc.Authorize(context.Background(), s, []domain.Check{{Action: domain.ParseAction(r.Action), ResourceID: r.ResourceID}})
		if err != nil {
			t.Fatal(err)
		}
		if d[0].Allowed != (r.Expected == "allow") || d[0].Rule != r.Rule {
			t.Errorf("row %d: %+v, want %s %s", i, d[0], r.Expected, r.Rule)
		}
	}
}

// TestAccessChangesReachTheOutboxWithTheirCommit checks the transactional
// outbox end to end: a role change writes an outbox row the relay
// publishes as a decodable AccessChanged, and a refused change writes none.
func TestAccessChangesReachTheOutboxWithTheirCommit(t *testing.T) {
	url := startPostgres(t)
	pool := newDatabase(t, url)
	store := postgres.NewStore(pool)
	hasher, _ := credential.NewHasher(credential.HasherOptions{Params: credential.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}})
	svc, _ := app.New(app.DefaultConfig(), app.Deps{Store: store, Hasher: hasher, Issuer: fakes.NewIssuer(time.Now)})
	ctx := context.Background()
	owner, _, err := svc.SignUp(ctx, "owner@example.com", "correct horse battery staple", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := svc.CreateWorkspace(ctx, domain.UserSubject(owner.ID, ""), "W")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RemoveMember(ctx, domain.UserSubject(owner.ID, ""), ws, owner.ID); err == nil {
		t.Fatal("removed the last owner")
	}
	bus := events.NewMemory(events.MemoryOptions{})
	bus.CreateTopic(identityevents.Topic)
	relay, err := outbox.NewRelay(outbox.NewPostgresStore(pool, outbox.PostgresStoreOptions{}), bus, outbox.RelayConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	published := bus.Published(identityevents.Topic)
	if len(published) != 1 || published[0].OrderingKey != string(ws) {
		t.Fatalf("published %d events (want 1, the workspace creation): %+v", len(published), published)
	}
}
