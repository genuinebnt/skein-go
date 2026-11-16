package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors from planning and applying migrations. Test with errors.Is.
var (
	ErrBadMigrationName = errors.New("postgres: migration file name must be NNNN_description.sql")
	ErrDuplicateVersion = errors.New("postgres: two migration files share a version")
	ErrChecksumMismatch = errors.New("postgres: applied migration file has changed")
	ErrMissingMigration = errors.New("postgres: applied migration file is missing")
	ErrOutOfOrder       = errors.New("postgres: new migration is older than the newest applied one")
)

// noTransactionMarker as the first line runs the file outside a transaction.
const noTransactionMarker = "-- skein:no-transaction"

// Migration is one parsed migration file.
type Migration struct {
	Version       int64
	Name          string
	SQL           string
	Checksum      string // hex SHA-256 of SQL
	NoTransaction bool
}

// AppliedMigration is a row of schema_migrations.
type AppliedMigration struct {
	Version  int64
	Checksum string
}

// ParseMigrationName splits "0007_add_comments.sql" into 7 and
// "add_comments". Versions are positive; descriptions are lower-case
// letters, digits and underscores, so file names sort and grep cleanly.
func ParseMigrationName(file string) (int64, string, error) {
	base, ok := strings.CutSuffix(file, ".sql")
	digits, name, found := strings.Cut(base, "_")
	if !ok || !found || digits == "" || name == "" || len(digits) > 18 {
		return 0, "", fmt.Errorf("%w: %q", ErrBadMigrationName, file)
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, "", fmt.Errorf("%w: %q", ErrBadMigrationName, file)
		}
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return 0, "", fmt.Errorf("%w: %q", ErrBadMigrationName, file)
		}
	}
	version, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || version <= 0 {
		return 0, "", fmt.Errorf("%w: %q", ErrBadMigrationName, file)
	}
	return version, name, nil
}

// LoadMigrations reads every .sql file in dir of fsys, sorted by version.
// Files that do not end in .sql are ignored (READMEs); a .sql file with a
// bad name is an error, because it would otherwise silently never run.
func LoadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("postgres: read migrations: %w", err)
	}
	var out []Migration
	seen := map[int64]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, err := ParseMigrationName(e.Name())
		if err != nil {
			return nil, err
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("%w: %s and %s", ErrDuplicateVersion, other, e.Name())
		}
		seen[version] = e.Name()
		data, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("postgres: read %s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(data)
		sql := string(data)
		out = append(out, Migration{
			Version: version, Name: name, SQL: sql, Checksum: hex.EncodeToString(sum[:]),
			NoTransaction: strings.HasPrefix(strings.TrimLeft(sql, " \t\r\n"), noTransactionMarker),
		})
	}
	slices.SortFunc(out, func(a, b Migration) int { return compareInt64(a.Version, b.Version) })
	return out, nil
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Plan returns the migrations still to apply, in order, or an error if the
// applied history and the files disagree. available must be sorted by
// version (LoadMigrations does this).
func Plan(available []Migration, applied []AppliedMigration) ([]Migration, error) {
	files := make(map[int64]Migration, len(available))
	for _, m := range available {
		files[m.Version] = m
	}
	done := make(map[int64]bool, len(applied))
	var newest int64
	for _, a := range applied {
		m, ok := files[a.Version]
		if !ok {
			return nil, fmt.Errorf("%w: version %d", ErrMissingMigration, a.Version)
		}
		if m.Checksum != a.Checksum {
			return nil, fmt.Errorf("%w: %04d_%s", ErrChecksumMismatch, m.Version, m.Name)
		}
		done[a.Version] = true
		newest = max(newest, a.Version)
	}
	var pending []Migration
	for _, m := range available {
		if done[m.Version] {
			continue
		}
		if m.Version < newest {
			return nil, fmt.Errorf("%w: %04d_%s is below applied version %d", ErrOutOfOrder, m.Version, m.Name, newest)
		}
		pending = append(pending, m)
	}
	return pending, nil
}

const createMigrationsTable = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    bigint PRIMARY KEY,
  name       text        NOT NULL,
  checksum   text        NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
)`

// lockMigrations serialises runners on the same schema. Services share one
// database locally, and advisory locks are database-wide, so the key must
// include the schema.
const lockMigrations = `SELECT pg_advisory_lock(hashtext('skein.migrations.' || current_schema()))`
const unlockMigrations = `SELECT pg_advisory_unlock(hashtext('skein.migrations.' || current_schema()))`

// Migrate applies every pending migration in dir of fsys. It is safe to run
// from every replica at startup: they take turns, and all but the first
// find nothing to do.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, dir string, log *slog.Logger) error {
	available, err := LoadMigrations(fsys, dir)
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("postgres: acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, lockMigrations); err != nil {
		return fmt.Errorf("postgres: migration lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), unlockMigrations) }()

	if _, err := conn.Exec(ctx, createMigrationsTable); err != nil {
		return fmt.Errorf("postgres: create schema_migrations: %w", err)
	}
	applied, err := readApplied(ctx, conn.Conn())
	if err != nil {
		return err
	}
	pending, err := Plan(available, applied)
	if err != nil {
		return err
	}
	for _, m := range pending {
		if err := applyMigration(ctx, conn.Conn(), m); err != nil {
			return err
		}
		if log != nil {
			log.InfoContext(ctx, "applied migration", slog.Int64("version", m.Version), slog.String("name", m.Name))
		}
	}
	return nil
}

func readApplied(ctx context.Context, conn *pgx.Conn) ([]AppliedMigration, error) {
	rows, err := conn.Query(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("postgres: read schema_migrations: %w", err)
	}
	applied, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AppliedMigration, error) {
		var a AppliedMigration
		err := row.Scan(&a.Version, &a.Checksum)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: read schema_migrations: %w", err)
	}
	return applied, nil
}

const recordMigration = `INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`

func applyMigration(ctx context.Context, conn *pgx.Conn, m Migration) error {
	label := fmt.Sprintf("%04d_%s", m.Version, m.Name)
	if m.NoTransaction {
		if _, err := conn.Exec(ctx, m.SQL); err != nil {
			return fmt.Errorf("postgres: migration %s: %w", label, err)
		}
		if _, err := conn.Exec(ctx, recordMigration, m.Version, m.Name, m.Checksum); err != nil {
			return fmt.Errorf("postgres: record migration %s: %w", label, err)
		}
		return nil
	}
	return WithTx(ctx, conn, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return fmt.Errorf("postgres: migration %s: %w", label, err)
		}
		if _, err := tx.Exec(ctx, recordMigration, m.Version, m.Name, m.Checksum); err != nil {
			return fmt.Errorf("postgres: record migration %s: %w", label, err)
		}
		return nil
	})
}
