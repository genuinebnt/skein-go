package postgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
)

// fakeTx records how a transaction ended. Embedding pgx.Tx satisfies the
// interface; calling any other method panics, which would mean WithTx did
// something it should not.
type fakeTx struct {
	pgx.Tx
	committed, rolledBack bool
	commitErr             error
}

func (t *fakeTx) Commit(context.Context) error {
	t.committed = true
	return t.commitErr
}

func (t *fakeTx) Rollback(context.Context) error {
	if t.committed {
		return pgx.ErrTxClosed
	}
	t.rolledBack = true
	return nil
}

type fakeDB struct {
	tx       *fakeTx
	beginErr error
}

func (d *fakeDB) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	if d.beginErr != nil {
		return nil, d.beginErr
	}
	return d.tx, nil
}

func TestWithTxCommitsOnSuccess(t *testing.T) {
	db := &fakeDB{tx: &fakeTx{}}
	if err := WithTx(context.Background(), db, func(context.Context, pgx.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !db.tx.committed || db.tx.rolledBack {
		t.Fatalf("committed=%v rolledBack=%v", db.tx.committed, db.tx.rolledBack)
	}
}

func TestWithTxRollsBackOnErrorAndReturnsIt(t *testing.T) {
	db := &fakeDB{tx: &fakeTx{}}
	want := errors.New("slug taken")
	err := WithTx(context.Background(), db, func(context.Context, pgx.Tx) error { return want })
	if !errors.Is(err, want) || db.tx.committed || !db.tx.rolledBack {
		t.Fatalf("err=%v committed=%v rolledBack=%v", err, db.tx.committed, db.tx.rolledBack)
	}
}

func TestWithTxRollsBackOnPanicAndRepanics(t *testing.T) {
	db := &fakeDB{tx: &fakeTx{}}
	defer func() {
		if p := recover(); p != "boom" {
			t.Fatalf("panic not propagated: %v", p)
		}
		if !db.tx.rolledBack || db.tx.committed {
			t.Fatal("panic did not roll back")
		}
	}()
	_ = WithTx(context.Background(), db, func(context.Context, pgx.Tx) error { panic("boom") })
}

func TestWithTxRollsBackEvenWhenContextIsCancelled(t *testing.T) {
	db := &fakeDB{tx: &fakeTx{}}
	ctx, cancel := context.WithCancel(context.Background())
	err := WithTx(ctx, db, func(context.Context, pgx.Tx) error {
		cancel()
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) || !db.tx.rolledBack {
		t.Fatalf("err=%v rolledBack=%v", err, db.tx.rolledBack)
	}
}

func TestWithTxReportsBeginAndCommitFailures(t *testing.T) {
	boom := errors.New("connection reset")
	if err := WithTx(context.Background(), &fakeDB{beginErr: boom}, func(context.Context, pgx.Tx) error {
		t.Fatal("fn ran without a transaction")
		return nil
	}); !errors.Is(err, boom) {
		t.Errorf("begin: %v", err)
	}
	db := &fakeDB{tx: &fakeTx{commitErr: boom}}
	if err := WithTx(context.Background(), db, func(context.Context, pgx.Tx) error { return nil }); !errors.Is(err, boom) {
		t.Errorf("commit: %v", err)
	}
}

func migrationFS(files map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys["migrations/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	return fsys
}

func TestLoadMigrationsSortsParsesAndFlagsNoTransaction(t *testing.T) {
	fsys := migrationFS(map[string]string{
		"0010_index_notes.sql": "-- skein:no-transaction\nCREATE INDEX CONCURRENTLY i ON notes (id);",
		"0002_notes.sql":       "CREATE TABLE notes (id uuid);",
		"0001_init.sql":        "SELECT 1;",
		"README.md":            "not a migration",
	})
	ms, err := LoadMigrations(fsys, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 3 || ms[0].Version != 1 || ms[1].Version != 2 || ms[2].Version != 10 {
		t.Fatalf("got %+v", ms)
	}
	if ms[1].NoTransaction || !ms[2].NoTransaction || ms[2].Name != "index_notes" || len(ms[0].Checksum) != 64 {
		t.Fatalf("got %+v", ms)
	}
}

func TestLoadMigrationsRejectsBadNamesAndDuplicates(t *testing.T) {
	if _, err := LoadMigrations(migrationFS(map[string]string{"1_a.sql": "", "001_b.sql": ""}), "migrations"); !errors.Is(err, ErrDuplicateVersion) {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := LoadMigrations(migrationFS(map[string]string{"create_notes.sql": ""}), "migrations"); !errors.Is(err, ErrBadMigrationName) {
		t.Errorf("bad name: %v", err)
	}
}

func TestParseMigrationName(t *testing.T) {
	good := map[string]int64{"0001_init.sql": 1, "42_add_comments_table.sql": 42, "20261116_x2.sql": 20261116}
	for file, want := range good {
		if v, _, err := ParseMigrationName(file); err != nil || v != want {
			t.Errorf("%s: %d %v", file, v, err)
		}
	}
	for _, file := range []string{"0000_zero.sql", "1_.sql", "_x.sql", "1_Upper.sql", "1-dash.sql", "1_x.SQL", "1_x.sql.bak", "-1_x.sql", "1_a b.sql", "9999999999999999999_big.sql"} {
		if _, _, err := ParseMigrationName(file); !errors.Is(err, ErrBadMigrationName) {
			t.Errorf("%s accepted", file)
		}
	}
}

func FuzzParseMigrationName(f *testing.F) {
	for _, s := range []string{"0001_init.sql", "1_.sql", "x", "00_a.sql", "1_a_b.sql", "١_a.sql"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, file string) {
		version, name, err := ParseMigrationName(file)
		if err != nil {
			return
		}
		if version <= 0 || name == "" || !strings.HasSuffix(file, "_"+name+".sql") {
			t.Fatalf("accepted %q as version %d name %q", file, version, name)
		}
	})
}

func mig(version int64, checksum string) Migration {
	return Migration{Version: version, Name: "m", Checksum: checksum}
}

func TestPlan(t *testing.T) {
	files := []Migration{mig(1, "a"), mig(2, "b"), mig(3, "c")}
	cases := map[string]struct {
		available []Migration
		applied   []AppliedMigration
		pending   []int64
		err       error
	}{
		"fresh database":   {files, nil, []int64{1, 2, 3}, nil},
		"partly applied":   {files, []AppliedMigration{{1, "a"}}, []int64{2, 3}, nil},
		"up to date":       {files, []AppliedMigration{{1, "a"}, {2, "b"}, {3, "c"}}, nil, nil},
		"edited file":      {files, []AppliedMigration{{1, "a"}, {2, "EDITED"}}, nil, ErrChecksumMismatch},
		"deleted file":     {files[1:], []AppliedMigration{{1, "a"}}, nil, ErrMissingMigration},
		"gap filled later": {files, []AppliedMigration{{1, "a"}, {3, "c"}}, nil, ErrOutOfOrder},
	}
	for name, c := range cases {
		pending, err := Plan(c.available, c.applied)
		if !errors.Is(err, c.err) {
			t.Errorf("%s: err %v, want %v", name, err, c.err)
			continue
		}
		var versions []int64
		for _, m := range pending {
			versions = append(versions, m.Version)
		}
		if !slices.Equal(versions, c.pending) {
			t.Errorf("%s: pending %v, want %v", name, versions, c.pending)
		}
	}
}
