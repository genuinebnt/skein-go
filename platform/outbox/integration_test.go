//go:build integration

package outbox

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/postgres"
)

func startPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("skein"), tcpostgres.WithUsername("skein"), tcpostgres.WithPassword("skein"),
		tcpostgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	url, _ := container.ConnectionString(ctx, "sslmode=disable")
	pool, err := postgres.Open(ctx, postgres.Config{URL: config.NewSecret(url)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migrations := fstest.MapFS{"m/0001_outbox.sql": {Data: []byte(SchemaSQL)}}
	if err := postgres.Migrate(ctx, pool, migrations, "m", nil); err != nil {
		t.Fatal(err)
	}
	return pool
}

func enqueueIn(t *testing.T, pool *pgxpool.Pool, key string, commit bool) events.Envelope {
	t.Helper()
	e, _ := events.NewEnvelope(context.Background(), "documents", key, []byte(key))
	err := postgres.WithTx(context.Background(), pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := Enqueue(ctx, tx, "documents.events", e); err != nil {
			return err
		}
		if !commit {
			return context.Canceled
		}
		return nil
	})
	if commit && err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEnqueueCommitsWithTheTransactionOnly(t *testing.T) {
	pool := startPostgres(t)
	enqueueIn(t, pool, "kept", true)
	enqueueIn(t, pool, "rolled-back", false)
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM outbox`).Scan(&n)
	if n != 1 {
		t.Fatalf("outbox has %d rows, want only the committed one", n)
	}
}

func TestRelayPublishesInOrderAndRespectsBackoff(t *testing.T) {
	pool := startPostgres(t)
	a1 := enqueueIn(t, pool, "a", true)
	a2 := enqueueIn(t, pool, "a", true)
	b1 := enqueueIn(t, pool, "b", true)
	store := NewPostgresStore(pool, PostgresStoreOptions{})
	failA1 := true
	pub := &recordingPublisher{fail: func(e events.Envelope) bool { return e.ID == a1.ID && failA1 }}
	relay, _ := NewRelay(store, pub, RelayConfig{InitialBackoff: 300 * time.Millisecond, Jitter: noJitter})

	res, err := relay.RunOnce(context.Background())
	if err != nil || res.Published != 1 || res.Failed != 1 || res.Skipped != 1 {
		t.Fatalf("first batch %+v %v: want b1 published, a1 failed, a2 held back", res, err)
	}
	if res, _ := relay.RunOnce(context.Background()); res.Claimed != 0 {
		t.Fatalf("claim ignored a1's backoff: %+v", res)
	}
	failA1 = false
	time.Sleep(350 * time.Millisecond)
	if _, err := relay.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range pub.sent {
		ids = append(ids, e.ID)
	}
	want := []string{b1.ID, a1.ID, a2.ID}
	if len(ids) != 3 || ids[0] != want[0] || ids[1] != want[1] || ids[2] != want[2] {
		t.Fatalf("published %v, want %v", ids, want)
	}
}

func TestSecondRelayClaimsNothingWhileFirstHoldsTheLock(t *testing.T) {
	pool := startPostgres(t)
	enqueueIn(t, pool, "a", true)
	store := NewPostgresStore(pool, PostgresStoreOptions{})
	first, err := store.Claim(context.Background(), 10)
	if err != nil || len(first.Records()) != 1 {
		t.Fatalf("first claim: %v %v", first, err)
	}
	second, err := store.Claim(context.Background(), 10)
	if err != nil || len(second.Records()) != 0 {
		t.Fatalf("second relay claimed %d rows", len(second.Records()))
	}
	_ = second.Rollback(context.Background())
	_ = first.Rollback(context.Background())

	shared := NewPostgresStore(pool, PostgresStoreOptions{Shared: true})
	held, _ := shared.Claim(context.Background(), 10)
	other, _ := shared.Claim(context.Background(), 10)
	if len(held.Records()) != 1 || len(other.Records()) != 0 {
		t.Fatalf("shared mode: SKIP LOCKED must hide the locked row (got %d and %d)", len(held.Records()), len(other.Records()))
	}
	_ = other.Rollback(context.Background())
	_ = held.Rollback(context.Background())
}

func TestProcessOnceRecordsEachEventOncePerConsumer(t *testing.T) {
	pool := startPostgres(t)
	c, _ := NewConsumer(pool, "search.documents-events", nil)
	msg := events.Message{Envelope: events.Envelope{ID: "0193a1b2-0000-7000-8000-000000000001", Source: "documents"}}
	effects := 0
	for range 3 {
		if err := c.HandleOnce(context.Background(), msg, func(context.Context, pgx.Tx, events.Message) error {
			effects++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if effects != 1 {
		t.Fatalf("effect ran %d times", effects)
	}
}

func TestPruneDeletesOnlyOldPublishedRows(t *testing.T) {
	pool := startPostgres(t)
	enqueueIn(t, pool, "a", true)
	enqueueIn(t, pool, "b", true)
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `UPDATE outbox SET published_at = now() - interval '8 days' WHERE ordering_key = 'a'`)
	n, err := NewPostgresStore(pool, PostgresStoreOptions{}).Prune(ctx, 7*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d (%v), want 1", n, err)
	}
}
