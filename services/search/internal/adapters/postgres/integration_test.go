//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/logging"
	platformpg "github.com/genuinebnt/skein-go/platform/postgres"
	"github.com/genuinebnt/skein-go/services/search/internal/adapters/postgres"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
	"github.com/genuinebnt/skein-go/services/search/internal/fakes"
	"github.com/genuinebnt/skein-go/services/search/migrations"
)

func startStore(t *testing.T) *postgres.Store {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("skein"), tcpostgres.WithUsername("skein"), tcpostgres.WithPassword("skein"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	url, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := platformpg.Open(ctx, platformpg.Config{URL: config.NewSecret(url)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := platformpg.Migrate(ctx, pool, migrations.FS, ".", logging.Discard()); err != nil {
		t.Fatal(err)
	}
	return postgres.NewStore(pool)
}

func states(t *testing.T, s app.Store) ([]*domain.NoteState, int64) {
	t.Helper()
	var out []*domain.NoteState
	position, err := s.LoadAll(context.Background(), func(n *domain.NoteState) error {
		out = append(out, n)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out, position
}

func sameStates(a, b []*domain.NoteState) bool {
	return slices.EqualFunc(a, b, func(x, y *domain.NoteState) bool {
		return x.ID == y.ID && x.WorkspaceID == y.WorkspaceID && x.Title == y.Title && x.Trashed == y.Trashed &&
			x.Root == y.Root && x.Purged == y.Purged && slices.Equal(x.Blocks, y.Blocks) && slices.Equal(x.Links, y.Links)
	})
}

// TestStoreMatchesTheInMemoryFake applies one random stream, with
// redeliveries, to Postgres and to the fake, and compares states, feeds and
// snapshot handling.
func TestStoreMatchesTheInMemoryFake(t *testing.T) {
	ctx := context.Background()
	pg, fake := startStore(t), fakes.NewStore()
	r := rand.New(rand.NewPCG(1, 2))
	var ids []string
	for i := range 400 {
		note := fmt.Sprint("n", r.IntN(8))
		m := domain.Mutation{NoteID: note}
		switch r.IntN(6) {
		case 0:
			m.Kind, m.WorkspaceID, m.Title = domain.MutationCreate, "w", fmt.Sprint("title ", i)
		case 1:
			m.Kind, m.Links = domain.MutationLinks, []string{fmt.Sprint("n", r.IntN(8)), note}
		case 2:
			m.Kind = []domain.MutationKind{domain.MutationTrash, domain.MutationRestore, domain.MutationPurge}[r.IntN(3)]
		default:
			m.Kind = domain.MutationBlocks
			m.Blocks = []domain.Block{{ID: fmt.Sprint("b", r.IntN(3)), Kind: "code", Language: "go", Text: fmt.Sprint("x := ", i)}}
			m.RemovedBlocks = []string{fmt.Sprint("b", r.IntN(3))}
		}
		id := uuid.NewString()
		ids = append(ids, id)
		for _, s := range []app.Store{pg, fake} {
			if _, err := s.Apply(ctx, app.DocumentsConsumer, id, &m); err != nil {
				t.Fatal(err)
			}
			if r.IntN(5) == 0 {
				again := ids[r.IntN(len(ids))]
				if first, err := s.Apply(ctx, app.DocumentsConsumer, again, &m); err != nil || first {
					t.Fatalf("redelivery of %s applied again (%v)", again, err)
				}
			}
		}
		if i == 200 {
			for _, s := range []app.Store{pg, fake} {
				if err := s.SaveSnapshot(ctx, app.Snapshot{Position: 200, Data: []byte("x")}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	got, gotPos := states(t, pg)
	want, wantPos := states(t, fake)
	if gotPos != wantPos || !sameStates(got, want) {
		t.Fatalf("postgres holds %d notes at %d, fake %d at %d", len(got), gotPos, len(want), wantPos)
	}
	gotFeed, _ := pg.Changes(ctx, 0, 1000)
	wantFeed, _ := fake.Changes(ctx, 0, 1000)
	if !slices.Equal(gotFeed, wantFeed) {
		t.Fatal("change feeds differ")
	}
	_, pgSnap, _ := pg.LatestSnapshot(ctx)
	_, fakeSnap, _ := fake.LatestSnapshot(ctx)
	if pgSnap != fakeSnap {
		t.Fatalf("snapshot present: postgres %v, fake %v (a purge after it must delete it)", pgSnap, fakeSnap)
	}
}

func TestIndexRestartsFromPostgresWithoutRebuilding(t *testing.T) {
	ctx := context.Background()
	store := startStore(t)
	apply := func(id string, m domain.Mutation) {
		if _, err := store.Apply(ctx, app.DocumentsConsumer, id, &m); err != nil {
			t.Fatal(err)
		}
	}
	apply(uuid.NewString(), domain.Mutation{Kind: domain.MutationCreate, NoteID: "a", WorkspaceID: "w", Title: "Raft"})
	apply(uuid.NewString(), domain.Mutation{Kind: domain.MutationBlocks, NoteID: "a", Blocks: []domain.Block{{ID: "1", Text: "log compaction"}}})
	first := app.NewIndex(store, app.IndexConfig{})
	if err := first.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Snapshot(ctx); err != nil {
		t.Fatal(err)
	}
	apply(uuid.NewString(), domain.Mutation{Kind: domain.MutationLinks, NoteID: "a", Links: []string{"b"}})
	second := app.NewIndex(store, app.IndexConfig{})
	if err := second.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if second.Position() != 3 {
		t.Fatalf("position %d", second.Position())
	}
	time.Sleep(10 * time.Millisecond)
	if n, err := store.PruneFeed(ctx, 0); err != nil || n != 2 {
		t.Fatalf("pruned %d (%v)", n, err)
	}
	if _, err := store.Changes(ctx, 0, 10); err == nil {
		t.Fatal("a follower behind the pruned feed was not told to reload")
	}
}
