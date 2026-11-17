//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/postgres/pgtest"
)

// startPostgres runs the same image as deploy/compose and returns a pool.
func startPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	// SKEIN_TEST_POSTGRES_DSN runs against an existing server (make
	// up-native) instead of a container; see platform/postgres/pgtest.
	url, ok := pgtest.FreshDatabase(t)
	if !ok {
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
	pool, err := Open(ctx, Config{URL: config.NewSecret(url)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestMigrateAppliesOnceAndDetectsEditedFiles(t *testing.T) {
	pool := startPostgres(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		"m/0001_notes.sql":            {Data: []byte("CREATE TABLE notes (id bigint PRIMARY KEY, title text NOT NULL);")},
		"m/0002_titles.sql":           {Data: []byte("ALTER TABLE notes ADD COLUMN slug text; CREATE INDEX notes_slug ON notes (slug);")},
		"m/0003_concurrent_index.sql": {Data: []byte("-- skein:no-transaction\nCREATE INDEX CONCURRENTLY notes_title ON notes (title);")},
	}
	for range 2 {
		if err := Migrate(ctx, pool, fsys, "m", logging.Discard()); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("schema_migrations has %d rows (%v), want 3", count, err)
	}
	fsys["m/0002_titles.sql"] = &fstest.MapFile{Data: []byte("-- edited\n")}
	if err := Migrate(ctx, pool, fsys, "m", logging.Discard()); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("edited migration: want ErrChecksumMismatch, got %v", err)
	}
}

func TestFailedMigrationLeavesNoTrace(t *testing.T) {
	pool := startPostgres(t)
	ctx := context.Background()
	fsys := fstest.MapFS{"m/0001_bad.sql": {Data: []byte("CREATE TABLE half (id int); SELECT no_such_function();")}}
	if err := Migrate(ctx, pool, fsys, "m", nil); err == nil {
		t.Fatal("bad migration succeeded")
	}
	var exists bool
	_ = pool.QueryRow(ctx, `SELECT to_regclass('half') IS NOT NULL`).Scan(&exists)
	if exists {
		t.Fatal("a failed migration left its table behind")
	}
}

func TestConcurrentMigrateRunsEachMigrationOnce(t *testing.T) {
	pool := startPostgres(t)
	fsys := fstest.MapFS{"m/0001_counter.sql": {Data: []byte("CREATE TABLE counter (n int); INSERT INTO counter VALUES (1);")}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- Migrate(context.Background(), pool, fsys, "m", nil) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM counter`).Scan(&rows)
	if rows != 1 {
		t.Fatalf("migration ran %d times", rows)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	pool := startPostgres(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE t (id int)`); err != nil {
		t.Fatal(err)
	}
	_ = WithTx(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO t VALUES (1)`); err != nil {
			return err
		}
		return errors.New("abort")
	})
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n)
	if n != 0 {
		t.Fatalf("rolled-back insert is visible")
	}
}
