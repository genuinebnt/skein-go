package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStoreOptions configure NewPostgresStore.
type PostgresStoreOptions struct {
	// Shared lets several relays claim batches at the same time. Per-key
	// order across relays is then no longer guaranteed; see the package
	// documentation.
	Shared bool
}

// PostgresStore is the Store over the service's outbox table.
type PostgresStore struct {
	pool *pgxpool.Pool
	opts PostgresStoreOptions
}

// NewPostgresStore returns a store on pool, whose role's search_path must
// contain the outbox table.
func NewPostgresStore(pool *pgxpool.Pool, opts PostgresStoreOptions) *PostgresStore {
	return &PostgresStore{pool: pool, opts: opts}
}

// The lock key includes the schema because services share one database
// locally and advisory locks are database-wide.
const tryRelayLock = `SELECT pg_try_advisory_xact_lock(hashtext('skein.outbox.' || current_schema()))`

// claimDue selects due rows in id order, skipping rows locked by others
// and rows that would overtake an earlier, still backing-off row of the
// same ordering key.
const claimDue = `
SELECT o.id, o.topic, o.event_id::text, o.source, o.ordering_key, o.traceparent,
       o.attributes, o.payload, o.attempts, o.created_at
FROM outbox o
WHERE o.published_at IS NULL
  AND o.next_attempt_at <= now()
  AND NOT EXISTS (
    SELECT 1 FROM outbox e
    WHERE o.ordering_key <> ''
      AND e.ordering_key = o.ordering_key
      AND e.published_at IS NULL
      AND e.id < o.id
      AND e.next_attempt_at > now())
ORDER BY o.id
LIMIT $1
FOR UPDATE OF o SKIP LOCKED`

// Claim implements Store.
func (s *PostgresStore) Claim(ctx context.Context, limit int) (Batch, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	b := &pgBatch{tx: tx}
	if !s.opts.Shared {
		var locked bool
		if err := tx.QueryRow(ctx, tryRelayLock).Scan(&locked); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
		if !locked {
			return b, nil // another relay is active; an empty batch
		}
	}
	rows, err := tx.Query(ctx, claimDue, limit)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	b.records, err = pgx.CollectRows(rows, scanRecord)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return b, nil
}

func scanRecord(row pgx.CollectableRow) (Record, error) {
	var r Record
	var attrs []byte
	err := row.Scan(&r.ID, &r.Topic, &r.Envelope.ID, &r.Envelope.Source, &r.Envelope.OrderingKey,
		&r.Envelope.Traceparent, &attrs, &r.Envelope.Payload, &r.Attempts, &r.CreatedAt)
	if err != nil {
		return Record{}, err
	}
	if err := json.Unmarshal(attrs, &r.Envelope.Attributes); err != nil {
		return Record{}, fmt.Errorf("outbox: row %d attributes: %w", r.ID, err)
	}
	if len(r.Envelope.Attributes) == 0 {
		r.Envelope.Attributes = nil
	}
	return r, nil
}

// Prune implements Pruner.
func (s *PostgresStore) Prune(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM outbox WHERE published_at < now() - $1::interval`, olderThan.String())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

type pgBatch struct {
	tx      pgx.Tx
	records []Record
}

func (b *pgBatch) Records() []Record { return b.records }

func (b *pgBatch) MarkPublished(ctx context.Context, ids []int64) error {
	_, err := b.tx.Exec(ctx, `UPDATE outbox SET published_at = now(), last_error = NULL WHERE id = ANY($1)`, ids)
	return err
}

func (b *pgBatch) MarkFailed(ctx context.Context, id int64, retryAfter time.Duration, reason string) error {
	_, err := b.tx.Exec(ctx, `
UPDATE outbox
SET attempts = attempts + 1,
    next_attempt_at = now() + $2 * interval '1 microsecond',
    last_error = left($3, 2000)
WHERE id = $1`, id, retryAfter.Microseconds(), reason)
	return err
}

func (b *pgBatch) Commit(ctx context.Context) error { return b.tx.Commit(ctx) }

func (b *pgBatch) Rollback(ctx context.Context) error {
	if err := b.tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return err
	}
	return nil
}

var (
	_ Store  = (*PostgresStore)(nil)
	_ Pruner = (*PostgresStore)(nil)
)
