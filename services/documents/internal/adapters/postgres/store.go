// Package postgres is the documents service's Store on Postgres: notes, the
// append-only op log, snapshots, the published projection state and the
// transactional outbox, in the documents schema (migrations/). Every write
// that has events enqueues them with platform/outbox in the same
// transaction (ADR-0006).
//
// Queries are runtime-checked pgx calls, as in the Rust port's sqlx. CRDT
// integers are unsigned 64-bit; they are stored in bigint by bit pattern.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/platform/outbox"
	"github.com/genuinebnt/skein-go/platform/postgres"
	"github.com/genuinebnt/skein-go/services/documents/internal/adapters/eventcodec"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/ports"
)

// Store implements ports.Store.
type Store struct {
	pool *pgxpool.Pool
	// Notify, when set, is called after a commit that enqueued events, so
	// the outbox relay publishes them without waiting for its poll.
	Notify func()
}

// NewStore returns a store on pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ ports.Store = (*Store)(nil)

func (s *Store) tx(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return postgres.WithTx(ctx, s.pool, fn)
}

func (s *Store) enqueue(ctx context.Context, tx pgx.Tx, events []domain.Event) error {
	for _, e := range events {
		env, err := eventcodec.Encode(ctx, e)
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, eventcodec.Topic, env); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) notified(err error, events []domain.Event) error {
	if err == nil && len(events) > 0 && s.Notify != nil {
		s.Notify()
	}
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// InsertNote implements ports.Store.
func (s *Store) InsertNote(ctx context.Context, n domain.Note, first *domain.Batch, p domain.Projection, events []domain.Event) error {
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		lastSeq := int64(0)
		if first != nil {
			lastSeq = first.Seq
		}
		tag, err := tx.Exec(ctx, `INSERT INTO notes (id, workspace_id, title, created_by, created_at, updated_at, trashed_at, last_seq)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (id) DO NOTHING`,
			n.ID, n.WorkspaceID, n.Title, n.CreatedBy, n.CreatedAt, n.UpdatedAt, nullTime(n.TrashedAt), lastSeq)
		if err != nil {
			return fmt.Errorf("insert note: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: note %s", domain.ErrAlreadyExists, n.ID)
		}
		if first != nil {
			if err := insertBatch(ctx, tx, n.ID, *first); err != nil {
				return err
			}
		}
		if err := upsertProjection(ctx, tx, n.ID, p); err != nil {
			return err
		}
		return s.enqueue(ctx, tx, events)
	})
	return s.notified(err, events)
}

const noteColumns = `id::text, workspace_id::text, title, created_by, created_at, updated_at, trashed_at`

func scanNote(row pgx.Row) (domain.Note, error) {
	var n domain.Note
	var id, ws string
	var trashed *time.Time
	if err := row.Scan(&id, &ws, &n.Title, &n.CreatedBy, &n.CreatedAt, &n.UpdatedAt, &trashed); err != nil {
		return n, err
	}
	n.ID, n.WorkspaceID = domain.NoteID(id), domain.WorkspaceID(ws)
	n.CreatedAt, n.UpdatedAt = n.CreatedAt.UTC(), n.UpdatedAt.UTC()
	if trashed != nil {
		n.TrashedAt = trashed.UTC()
	}
	return n, nil
}

// GetNote implements ports.Store.
func (s *Store) GetNote(ctx context.Context, id domain.NoteID) (domain.Note, error) {
	n, err := scanNote(s.pool.QueryRow(ctx, `SELECT `+noteColumns+` FROM notes WHERE id = $1 AND purged_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return n, fmt.Errorf("%w: note %s", domain.ErrNotFound, id)
	}
	return n, err
}

// ListNotes implements ports.Store.
func (s *Store) ListNotes(ctx context.Context, q ports.NoteQuery) ([]domain.Note, error) {
	ids := make([]string, len(q.IDs))
	for i, id := range q.IDs {
		ids[i] = string(id)
	}
	after := string(q.After)
	if after == "" {
		after = "00000000-0000-0000-0000-000000000000"
	}
	rows, err := s.pool.Query(ctx, `SELECT `+noteColumns+` FROM notes
		WHERE workspace_id = $1 AND id = ANY($2::uuid[]) AND id > $3 AND purged_at IS NULL
		  AND ($4 OR trashed_at IS NULL)
		ORDER BY id LIMIT $5`, q.Workspace, ids, after, q.IncludeTrashed, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	defer rows.Close()
	var out []domain.Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UpdateNote implements ports.Store.
func (s *Store) UpdateNote(ctx context.Context, n domain.Note, events []domain.Event) error {
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE notes SET title = $2, updated_at = $3, trashed_at = $4 WHERE id = $1 AND purged_at IS NULL`,
			n.ID, n.Title, n.UpdatedAt, nullTime(n.TrashedAt))
		if err != nil {
			return fmt.Errorf("update note: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: note %s", domain.ErrNotFound, n.ID)
		}
		return s.enqueue(ctx, tx, events)
	})
	return s.notified(err, events)
}

func insertBatch(ctx context.Context, tx pgx.Tx, id domain.NoteID, b domain.Batch) error {
	var ops, attrs []byte
	if len(b.Ops) > 0 {
		ops = crdt.EncodeOps(b.Ops)
	}
	if len(b.Attrs) > 0 {
		attrs = blocks.EncodeAttrs(b.Attrs)
	}
	_, err := tx.Exec(ctx, `INSERT INTO op_batches (note_id, seq, replica_id, first_counter, last_counter, for_replica,
		group_seq, user_id, agent_run_id, kind, ref_seq, at, ops, attrs)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		id, b.Seq, int64(b.Replica), int64(b.First), int64(b.Last), int64(b.For), b.Group, b.UserID, b.AgentRunID,
		int16(b.Kind), b.Ref, b.At, ops, attrs)
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: batch %d of note %s exists", domain.ErrConflict, b.Seq, id)
	}
	if err != nil {
		return fmt.Errorf("insert batch: %w", err)
	}
	return nil
}

// AppendBatches implements ports.Store.
func (s *Store) AppendBatches(ctx context.Context, id domain.NoteID, batches []domain.Batch, updatedAt time.Time) error {
	return s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		for _, b := range batches {
			if err := b.Validate(); err != nil {
				return err
			}
			if err := insertBatch(ctx, tx, id, b); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `UPDATE notes SET updated_at = $2, last_seq = $3 WHERE id = $1`, id, updatedAt, batches[len(batches)-1].Seq)
		return err
	})
}

const batchColumns = `seq, replica_id, first_counter, last_counter, for_replica, group_seq, user_id, agent_run_id, kind, ref_seq, at`

func scanBatch(row pgx.Row, withPayload bool) (domain.Batch, error) {
	var b domain.Batch
	var replica, first, last, forReplica int64
	var kind int16
	var ops, attrs []byte
	dest := []any{&b.Seq, &replica, &first, &last, &forReplica, &b.Group, &b.UserID, &b.AgentRunID, &kind, &b.Ref, &b.At}
	if withPayload {
		dest = append(dest, &ops, &attrs)
	}
	if err := row.Scan(dest...); err != nil {
		return b, err
	}
	b.Replica, b.First, b.Last, b.For = crdt.ReplicaID(replica), uint64(first), uint64(last), crdt.ReplicaID(forReplica)
	b.Kind, b.At = domain.BatchKind(kind), b.At.UTC()
	var err error
	if len(ops) > 0 {
		if b.Ops, err = crdt.DecodeOps(ops); err != nil {
			return b, fmt.Errorf("batch %d: %w", b.Seq, err)
		}
	}
	if len(attrs) > 0 {
		if b.Attrs, err = blocks.DecodeAttrs(attrs); err != nil {
			return b, fmt.Errorf("batch %d: %w", b.Seq, err)
		}
	}
	return b, nil
}

// LoadBatches implements ports.Store. Payloads before the snapshot are not
// read at all: history needs only their metadata.
func (s *Store) LoadBatches(ctx context.Context, id domain.NoteID, payloadsAfter int64) ([]domain.Batch, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+batchColumns+`,
		CASE WHEN seq > $2 THEN ops END, CASE WHEN seq > $2 THEN attrs END
		FROM op_batches WHERE note_id = $1 ORDER BY seq`, id, payloadsAfter)
	if err != nil {
		return nil, fmt.Errorf("load batches: %w", err)
	}
	defer rows.Close()
	var out []domain.Batch
	for rows.Next() {
		b, err := scanBatch(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// LoadBatch implements ports.Store.
func (s *Store) LoadBatch(ctx context.Context, id domain.NoteID, seq int64) (domain.Batch, error) {
	b, err := scanBatch(s.pool.QueryRow(ctx, `SELECT `+batchColumns+`, ops, attrs FROM op_batches WHERE note_id = $1 AND seq = $2`, id, seq), true)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, fmt.Errorf("%w: batch %d of note %s", domain.ErrNotFound, seq, id)
	}
	return b, err
}

// SaveSnapshot implements ports.Store.
func (s *Store) SaveSnapshot(ctx context.Context, id domain.NoteID, snap domain.Snapshot) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO snapshots (note_id, through_seq, ops, attrs, created_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (note_id, through_seq) DO NOTHING`,
		id, snap.ThroughSeq, crdt.EncodeOps(snap.Ops), blocks.EncodeAttrs(snap.Attrs), snap.At)
	if err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	// Older snapshots are superseded; keeping one spare guards against a
	// snapshot that turns out unreadable.
	_, err = s.pool.Exec(ctx, `DELETE FROM snapshots WHERE note_id = $1 AND through_seq < (
		SELECT min(through_seq) FROM (SELECT through_seq FROM snapshots WHERE note_id = $1 ORDER BY through_seq DESC LIMIT 2) newest)`, id)
	return err
}

// LatestSnapshot implements ports.Store.
func (s *Store) LatestSnapshot(ctx context.Context, id domain.NoteID) (domain.Snapshot, bool, error) {
	var snap domain.Snapshot
	var ops, attrs []byte
	err := s.pool.QueryRow(ctx, `SELECT through_seq, ops, attrs, created_at FROM snapshots WHERE note_id = $1 ORDER BY through_seq DESC LIMIT 1`, id).
		Scan(&snap.ThroughSeq, &ops, &attrs, &snap.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return snap, false, nil
	}
	if err != nil {
		return snap, false, fmt.Errorf("load snapshot: %w", err)
	}
	if snap.Ops, err = crdt.DecodeOps(ops); err != nil {
		return snap, false, fmt.Errorf("snapshot of note %s: %w", id, err)
	}
	if snap.Attrs, err = blocks.DecodeAttrs(attrs); err != nil {
		return snap, false, fmt.Errorf("snapshot of note %s: %w", id, err)
	}
	return snap, true, nil
}

// LoadProjection implements ports.Store.
func (s *Store) LoadProjection(ctx context.Context, id domain.NoteID) (domain.Projection, error) {
	var p domain.Projection
	var digests []byte
	err := s.pool.QueryRow(ctx, `SELECT through_seq, digests, links FROM projections WHERE note_id = $1`, id).Scan(&p.ThroughSeq, &digests, &p.Links)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, fmt.Errorf("load projection: %w", err)
	}
	return p, json.Unmarshal(digests, &p.Digests)
}

func upsertProjection(ctx context.Context, tx pgx.Tx, id domain.NoteID, p domain.Projection) error {
	_, err := writeProjection(ctx, tx, id, p)
	return err
}

func writeProjection(ctx context.Context, tx pgx.Tx, id domain.NoteID, p domain.Projection) (bool, error) {
	digests, err := json.Marshal(p.Digests)
	if err != nil {
		return false, err
	}
	links := p.Links
	if links == nil {
		links = []string{}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO projections (note_id, through_seq, digests, links) VALUES ($1, $2, $3, $4)
		ON CONFLICT (note_id) DO UPDATE SET through_seq = excluded.through_seq, digests = excluded.digests, links = excluded.links
		WHERE projections.through_seq <= excluded.through_seq`, id, p.ThroughSeq, digests, links)
	if err != nil {
		return false, fmt.Errorf("write projection: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// PublishProjection implements ports.Store.
func (s *Store) PublishProjection(ctx context.Context, id domain.NoteID, p domain.Projection, events []domain.Event) (bool, error) {
	wrote := false
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if wrote, err = writeProjection(ctx, tx, id, p); err != nil || !wrote {
			return err
		}
		return s.enqueue(ctx, tx, events)
	})
	if !wrote {
		events = nil
	}
	return wrote, s.notified(err, events)
}

// StaleProjections implements ports.Store.
func (s *Store) StaleProjections(ctx context.Context, limit int) ([]domain.NoteID, error) {
	return s.ids(ctx, `SELECT n.id::text FROM notes n LEFT JOIN projections p ON p.note_id = n.id
		WHERE n.purged_at IS NULL AND n.last_seq > coalesce(p.through_seq, 0) ORDER BY n.id LIMIT $1`, limit)
}

// TrashedBefore implements ports.Store.
func (s *Store) TrashedBefore(ctx context.Context, t time.Time, limit int) ([]domain.NoteID, error) {
	return s.ids(ctx, `SELECT id::text FROM notes WHERE trashed_at < $2 AND purged_at IS NULL ORDER BY trashed_at LIMIT $1`, limit, t)
}

func (s *Store) ids(ctx context.Context, sql string, args ...any) ([]domain.NoteID, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.NoteID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, domain.NoteID(id))
	}
	return out, rows.Err()
}

// PurgeNote implements ports.Store.
func (s *Store) PurgeNote(ctx context.Context, id domain.NoteID, events []domain.Event) error {
	err := s.tx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		for _, q := range []string{
			`DELETE FROM op_batches WHERE note_id = $1`,
			`DELETE FROM snapshots WHERE note_id = $1`,
			`DELETE FROM projections WHERE note_id = $1`,
			`UPDATE notes SET title = '', purged_at = now(), last_seq = 0 WHERE id = $1`,
		} {
			if _, err := tx.Exec(ctx, q, id); err != nil {
				return fmt.Errorf("purge: %w", err)
			}
		}
		return s.enqueue(ctx, tx, events)
	})
	return s.notified(err, events)
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
