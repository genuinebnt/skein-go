// Package postgres opens traced pgx v5 connection pools, runs functions in
// transactions, and applies a service's embedded SQL migrations.
//
// # Pools
//
// Open parses a postgres:// URL (from config.Secret), installs the otelpgx
// tracer (a span per query, with the SQL text but never the bound
// parameters, so note contents stay out of traces) and pool metrics, and
// pings before returning so a bad URL fails at startup. Each service
// connects as its own login role, whose search_path is its own schema
// (deploy/compose/postgres/init.sql), so queries name tables unqualified.
//
// # Transactions
//
// WithTx(ctx, db, fn) begins, runs fn and commits; if fn returns an error
// or panics it rolls back (re-panicking after). The transactional outbox
// and idempotent consumers rely on it: the state change, the outbox row
// and the processed-event record are written by one fn.
//
// # Migrations: why a small runner instead of goose
//
// Migrate applies files named NNNN_description.sql from an fs.FS (usually
// an embed.FS in the service) in version order, each in its own
// transaction together with its row in schema_migrations. goose would do
// this too, but it works through database/sql, so a service would hold a
// second driver stack next to pgx; it also brings down-migrations and Go
// migrations that this project does not want (migrations are forward-only:
// a mistake is fixed by a new migration). The runner here is about 150
// lines, uses the pgx pool the service already has, and has the semantics
// of sqlx's migrator, which the Rust port will use:
//
//   - forward-only, applied in ascending version order;
//   - a checksum (SHA-256 of the file) is stored, and an applied migration
//     whose file has changed is an error, never silently skipped;
//   - an applied version whose file is gone is an error;
//   - a new file whose version is below the newest applied one is an error
//     (ErrOutOfOrder), because it means two branches picked overlapping
//     numbers and the order they run in would differ between databases;
//   - a session advisory lock keyed by the current schema makes concurrent
//     replicas wait for each other instead of racing;
//   - a file whose first line is "-- skein:no-transaction" runs outside a
//     transaction, for statements like CREATE INDEX CONCURRENTLY. Keep such
//     files to a single statement, since a failure part-way cannot roll
//     back.
//
// Planning (which files to run) is a pure function, unit-tested and fuzzed;
// applying is tested against real Postgres in the integration suite.
//
// # Porting notes
//
// Rust: `sqlx::PgPool` with `tracing` spans; `pool.begin()` and an explicit
// commit replace WithTx. For migrations either keep this table and port the
// runner (it is small), or switch to `sqlx::migrate!`, which stores the
// same facts (version, checksum) in `_sqlx_migrations`, with a one-off
// migration that copies the rows across.
package postgres
