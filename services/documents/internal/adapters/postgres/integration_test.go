//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/postgres"
	"github.com/genuinebnt/skein-go/services/documents/internal/app"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/fakes"
	"github.com/genuinebnt/skein-go/services/documents/internal/seed"
	"github.com/genuinebnt/skein-go/services/documents/migrations"
)

// startPostgres returns a migrated pool on a fresh schema. With
// SKEIN_TEST_DATABASE_URL set it uses that server (no Docker needed);
// otherwise it starts the compose image with testcontainers.
func startPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("SKEIN_TEST_DATABASE_URL")
	if url == "" {
		container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
			tcpostgres.WithDatabase("skein"), tcpostgres.WithUsername("skein"), tcpostgres.WithPassword("skein"),
			tcpostgres.BasicWaitStrategies())
		if err != nil {
			t.Fatalf("start postgres: %v", err)
		}
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
		if url, err = container.ConnectionString(ctx, "sslmode=disable"); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := postgres.Open(ctx, postgres.Config{URL: config.NewSecret(url)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("documents_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := postgres.Open(ctx, postgres.Config{URL: config.NewSecret(url + sep + "search_path=" + schema)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool, migrations.FS, ".", logging.Discard()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestStoreRoundTripsTheOpLogAndRefusesASecondWriter(t *testing.T) {
	ctx := context.Background()
	s := NewStore(startPostgres(t))
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	note := domain.Note{ID: "019cc260-0a80-72dd-9e8d-ba3c76c67531", WorkspaceID: "019c8cf2-1000-769b-b000-73aede463976", Title: "t", CreatedBy: "u", CreatedAt: at, UpdatedAt: at}
	if err := s.InsertNote(ctx, note, nil, domain.Projection{}, []domain.Event{{Kind: domain.EventNoteCreated, NoteID: note.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertNote(ctx, note, nil, domain.Projection{}, nil); !errors.Is(err, domain.ErrAlreadyExists) {
		t.Fatalf("second insert: %v", err)
	}
	seq, _ := crdt.New(1 << 63) // a replica above 2^63 checks the bigint bit-pattern mapping
	ops, _ := seq.Insert(0, "\u001ehi")
	b := domain.Batch{Seq: 1, Replica: 1 << 63, First: 1, Last: 3, For: 1 << 63, UserID: "u", Kind: domain.KindEdit, At: at, Ops: ops}
	if err := s.AppendBatches(ctx, note.ID, []domain.Batch{b}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendBatches(ctx, note.ID, []domain.Batch{b}, at); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a second writer must conflict: %v", err)
	}
	got, err := s.LoadBatch(ctx, note.ID, 1)
	if err != nil || got.Replica != 1<<63 || len(got.Ops) != 3 || got.Ops[0] != ops[0] {
		t.Fatalf("got %+v %v", got, err)
	}
	stale, _ := s.StaleProjections(ctx, 10)
	if len(stale) != 1 {
		t.Fatalf("stale %v", stale)
	}
	if wrote, err := s.PublishProjection(ctx, note.ID, domain.Projection{ThroughSeq: 1}, nil); err != nil || !wrote {
		t.Fatal(wrote, err)
	}
	if wrote, _ := s.PublishProjection(ctx, note.ID, domain.Projection{ThroughSeq: 0}, nil); wrote {
		t.Fatal("an older projection must not overwrite a newer one")
	}
}

// The small seed through the real store: every note matches its CRDT
// truth, and a restarted service reloads the same notes from Postgres.
func TestSmallSeedLoadsIntoPostgres(t *testing.T) {
	ctx := context.Background()
	store := NewStore(startPostgres(t))
	d, err := seed.Open("../../../../../testdata/seed/small")
	if err != nil {
		t.Fatal(err)
	}
	clock := &seed.Clock{}
	policy, _ := seed.NewPolicy(d, clock)
	replicas, _ := seed.NewReplicas(d)
	newService := func() *app.Service {
		return app.New(app.Deps{Store: store, Authorizer: policy, Replicas: replicas, NoteCache: fakes.NewNoteCache(),
			Presence: fakes.NewPresence(clock), Clock: clock}, app.Config{SnapshotEvery: 500})
	}
	svc := newService()
	if _, err := seed.Load(ctx, d, svc, clock, logging.Discard()); err != nil {
		t.Fatal(err)
	}
	if err := seed.Verify(ctx, d, svc); err != nil {
		t.Fatal(err)
	}
	if err := seed.Verify(ctx, d, newService()); err != nil {
		t.Fatalf("after reloading from Postgres: %v", err)
	}
}
