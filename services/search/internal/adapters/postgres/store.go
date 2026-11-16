// Package postgres implements app.Store on the search schema: note states,
// the change feed, processed events and snapshots (migrations/).
//
// Apply reuses domain.ApplyMutation: it loads the note's state, computes
// the next one with the same pure function the in-memory fake uses, and
// writes the difference. The behaviour therefore lives in one place, and
// the integration test checks this adapter against the fake.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/genuinebnt/skein-go/platform/outbox"
	"github.com/genuinebnt/skein-go/platform/postgres"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
)

// keepSnapshots is how many snapshots SaveSnapshot keeps: the newest, and
// one before it in case the newest turns out unreadable.
const keepSnapshots = 2

// Store is the Postgres app.Store.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a store on pool, whose search_path is the search schema.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ app.Store = (*Store)(nil)

// Apply implements app.Store.
func (s *Store) Apply(ctx context.Context, consumer, eventID string, m *domain.Mutation) (bool, error) {
	first := false
	err := postgres.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if first, err = outbox.ProcessOnce(ctx, tx, consumer, eventID); err != nil || !first {
			return err
		}
		if m != nil {
			if err := applyMutation(ctx, tx, m); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `
INSERT INTO projection_positions (consumer, last_event_id, events_applied, updated_at)
VALUES ($1, $2, 1, now())
ON CONFLICT (consumer) DO UPDATE
SET last_event_id = EXCLUDED.last_event_id, events_applied = projection_positions.events_applied + 1, updated_at = now()`,
			consumer, eventID)
		return err
	})
	return first, err
}

func applyMutation(ctx context.Context, tx pgx.Tx, m *domain.Mutation) error {
	// Taking the next position first serializes every writer on the
	// feed_state row until commit (see the migration).
	var seq int64
	if err := tx.QueryRow(ctx, `UPDATE feed_state SET last_seq = last_seq + 1 WHERE id = 1 RETURNING last_seq`).Scan(&seq); err != nil {
		return fmt.Errorf("next feed position: %w", err)
	}
	states, err := loadNotes(ctx, tx, []string{m.NoteID})
	if err != nil {
		return err
	}
	before := states[m.NoteID]
	after, err := domain.ApplyMutation(before, *m)
	if err != nil {
		return err
	}
	if err := saveNote(ctx, tx, before, after); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO changes (seq, note_id) VALUES ($1, $2)`, seq, m.NoteID); err != nil {
		return fmt.Errorf("append change: %w", err)
	}
	if m.Kind == domain.MutationPurge {
		// Snapshots hold note text; erasure must not leave a copy behind.
		if _, err := tx.Exec(ctx, `DELETE FROM snapshots`); err != nil {
			return fmt.Errorf("delete snapshots: %w", err)
		}
	}
	return nil
}

// saveNote writes the difference between before (nil when new) and after.
func saveNote(ctx context.Context, tx pgx.Tx, before, after *domain.NoteState) error {
	if _, err := tx.Exec(ctx, `
INSERT INTO notes (note_id, workspace_id, title, trashed, is_root, purged) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (note_id) DO UPDATE SET workspace_id = $2, title = $3, trashed = $4, is_root = $5, purged = $6`,
		after.ID, after.WorkspaceID, after.Title, after.Trashed, after.Root, after.Purged); err != nil {
		return fmt.Errorf("save note: %w", err)
	}
	old := map[string]domain.Block{}
	var oldLinks []string
	if before != nil {
		for _, b := range before.Blocks {
			old[b.ID] = b
		}
		oldLinks = before.Links
	}
	var removed []string
	kept := map[string]bool{}
	for _, b := range after.Blocks {
		kept[b.ID] = true
	}
	for id := range old {
		if !kept[id] {
			removed = append(removed, id)
		}
	}
	if len(removed) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM blocks WHERE note_id = $1 AND block_id = ANY($2)`, after.ID, removed); err != nil {
			return fmt.Errorf("delete blocks: %w", err)
		}
	}
	var ids, kinds, langs, bodies []string
	var ordinals []int32
	for _, b := range after.Blocks {
		if prev, ok := old[b.ID]; ok && prev == b {
			continue
		}
		ids, kinds, langs, bodies = append(ids, b.ID), append(kinds, b.Kind), append(langs, b.Language), append(bodies, b.Text)
		ordinals = append(ordinals, int32(b.Ordinal))
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `
INSERT INTO blocks (note_id, block_id, ordinal, kind, language, body)
SELECT $1, * FROM unnest($2::text[], $3::int[], $4::text[], $5::text[], $6::text[])
ON CONFLICT (note_id, block_id) DO UPDATE
SET ordinal = EXCLUDED.ordinal, kind = EXCLUDED.kind, language = EXCLUDED.language, body = EXCLUDED.body`,
			after.ID, ids, ordinals, kinds, langs, bodies); err != nil {
			return fmt.Errorf("save blocks: %w", err)
		}
	}
	if !slices.Equal(oldLinks, after.Links) {
		if _, err := tx.Exec(ctx, `DELETE FROM links WHERE note_id = $1`, after.ID); err != nil {
			return fmt.Errorf("delete links: %w", err)
		}
		if len(after.Links) > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO links (note_id, target_id) SELECT $1, unnest($2::text[])`, after.ID, after.Links); err != nil {
				return fmt.Errorf("save links: %w", err)
			}
		}
	}
	return nil
}

// querier is what loadNotes needs from a pool or a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// loadNotes reads the states of ids. A nil ids loads every note.
func loadNotes(ctx context.Context, q querier, ids []string) (map[string]*domain.NoteState, error) {
	filter, args := "", []any{}
	if ids != nil {
		filter, args = " WHERE note_id = ANY($1)", []any{ids}
	}
	out := map[string]*domain.NoteState{}
	rows, err := q.Query(ctx, `SELECT note_id, workspace_id, title, trashed, is_root, purged FROM notes`+filter+` ORDER BY note_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("load notes: %w", err)
	}
	for rows.Next() {
		var n domain.NoteState
		if err := rows.Scan(&n.ID, &n.WorkspaceID, &n.Title, &n.Trashed, &n.Root, &n.Purged); err != nil {
			rows.Close()
			return nil, err
		}
		out[n.ID] = &n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT note_id, block_id, ordinal, kind, language, body FROM blocks`+filter+` ORDER BY note_id, ordinal`, args...)
	if err != nil {
		return nil, fmt.Errorf("load blocks: %w", err)
	}
	for rows.Next() {
		var note string
		var b domain.Block
		var ordinal int32
		if err := rows.Scan(&note, &b.ID, &ordinal, &b.Kind, &b.Language, &b.Text); err != nil {
			rows.Close()
			return nil, err
		}
		b.Ordinal = int(ordinal)
		if n := out[note]; n != nil {
			n.Blocks = append(n.Blocks, b)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT note_id, target_id FROM links`+filter+` ORDER BY note_id, target_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("load links: %w", err)
	}
	for rows.Next() {
		var note, target string
		if err := rows.Scan(&note, &target); err != nil {
			rows.Close()
			return nil, err
		}
		if n := out[note]; n != nil {
			n.Links = append(n.Links, target)
		}
	}
	return out, rows.Err()
}

// Changes implements app.Store.
func (s *Store) Changes(ctx context.Context, after int64, limit int) ([]app.Change, error) {
	var pruned int64
	if err := s.pool.QueryRow(ctx, `SELECT pruned_through FROM feed_state WHERE id = 1`).Scan(&pruned); err != nil {
		return nil, fmt.Errorf("read feed state: %w", err)
	}
	if after < pruned {
		return nil, fmt.Errorf("%w: at %d, pruned through %d", app.ErrFeedGap, after, pruned)
	}
	rows, err := s.pool.Query(ctx, `SELECT seq, note_id FROM changes WHERE seq > $1 ORDER BY seq LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read changes: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (app.Change, error) {
		var c app.Change
		err := r.Scan(&c.Seq, &c.NoteID)
		return c, err
	})
}

// LoadNotes implements app.Store.
func (s *Store) LoadNotes(ctx context.Context, ids []string) (map[string]*domain.NoteState, error) {
	if len(ids) == 0 {
		return map[string]*domain.NoteState{}, nil
	}
	return loadNotes(ctx, s.pool, ids)
}

// LoadAll implements app.Store. It reads in one repeatable-read
// transaction, so the notes and the position agree.
func (s *Store) LoadAll(ctx context.Context, fn func(*domain.NoteState) error) (int64, error) {
	var position int64
	err := postgres.WithTxOptions(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `SELECT last_seq FROM feed_state WHERE id = 1`).Scan(&position); err != nil {
				return fmt.Errorf("read feed state: %w", err)
			}
			notes, err := loadNotes(ctx, tx, nil)
			if err != nil {
				return err
			}
			ids := make([]string, 0, len(notes))
			for id := range notes {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			for _, id := range ids {
				if err := fn(notes[id]); err != nil {
					return err
				}
			}
			return nil
		})
	return position, err
}

// LatestSnapshot implements app.Store.
func (s *Store) LatestSnapshot(ctx context.Context) (app.Snapshot, bool, error) {
	var snap app.Snapshot
	err := s.pool.QueryRow(ctx, `SELECT position, data FROM snapshots ORDER BY position DESC LIMIT 1`).Scan(&snap.Position, &snap.Data)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Snapshot{}, false, nil
	}
	if err != nil {
		return app.Snapshot{}, false, fmt.Errorf("read snapshot: %w", err)
	}
	return snap, true, nil
}

// SaveSnapshot implements app.Store.
func (s *Store) SaveSnapshot(ctx context.Context, snap app.Snapshot) error {
	return postgres.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
INSERT INTO snapshots (position, format, data) VALUES ($1, 1, $2)
ON CONFLICT (position) DO UPDATE SET data = EXCLUDED.data, created_at = now()`, snap.Position, snap.Data); err != nil {
			return fmt.Errorf("save snapshot: %w", err)
		}
		_, err := tx.Exec(ctx, `
DELETE FROM snapshots WHERE position NOT IN (SELECT position FROM snapshots ORDER BY position DESC LIMIT $1)`, keepSnapshots)
		return err
	})
}

// PruneFeed deletes changes older than retention that the newest snapshot
// already covers, and records how far it pruned. Followers behind that
// point reload every note (app.ErrFeedGap). It returns the rows deleted.
func (s *Store) PruneFeed(ctx context.Context, retention time.Duration) (int64, error) {
	var deleted int64
	err := postgres.WithTx(ctx, s.pool, func(ctx context.Context, tx pgx.Tx) error {
		var through *int64
		if err := tx.QueryRow(ctx, `
SELECT max(seq) FROM changes
WHERE changed_at < now() - make_interval(secs => $1)
  AND seq <= (SELECT coalesce(max(position), 0) FROM snapshots)`, retention.Seconds()).Scan(&through); err != nil {
			return err
		}
		if through == nil {
			return nil
		}
		tag, err := tx.Exec(ctx, `DELETE FROM changes WHERE seq <= $1`, *through)
		if err != nil {
			return err
		}
		deleted = tag.RowsAffected()
		_, err = tx.Exec(ctx, `UPDATE feed_state SET pruned_through = greatest(pruned_through, $1) WHERE id = 1`, *through)
		return err
	})
	return deleted, err
}
