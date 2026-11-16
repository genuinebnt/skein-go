// Package postgres implements app.Store on the service's Postgres schema
// (migrations/0001_init.sql) with pgx, through platform/postgres for pools,
// transactions and migrations, and platform/outbox.ProcessOnce for the
// idempotent-consumer record.
//
// # Design
//
// Every app transaction is one Postgres transaction at READ COMMITTED.
// Writes that must not race use the schema's constraints rather than
// locks: the (recipient, source) unique key makes a concurrent duplicate
// insert fail (the event is then retried and finds the existing row), and
// the partial unique index on unassigned email jobs makes EnqueueEmail's
// "at most one" an ON CONFLICT DO NOTHING. Email sends are claimed with
// FOR UPDATE SKIP LOCKED, so replicas never claim the same send and never
// wait for each other.
//
// No pgx type leaves this package: rows become domain and app values here.
//
// # Porting notes
//
// Rust: the same SQL through sqlx (runtime-checked queries). Nullable
// columns map to Option; uuid columns to uuid::Uuid, converted to strings
// at the boundary as here.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/genuinebnt/skein-go/platform/outbox"
	platformpg "github.com/genuinebnt/skein-go/platform/postgres"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

// Store is app.Store on a pgx pool (or anything that begins transactions).
type Store struct {
	db platformpg.Beginner
}

// NewStore returns a store on db.
func NewStore(db platformpg.Beginner) *Store { return &Store{db: db} }

// WithTx implements app.Store.
func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context, tx app.Tx) error) error {
	return platformpg.WithTx(ctx, s.db, func(ctx context.Context, tx pgx.Tx) error {
		return fn(ctx, &txn{tx: tx})
	})
}

type txn struct{ tx pgx.Tx }

// uniqueViolation is SQLSTATE 23505.
func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func (t *txn) MarkProcessed(ctx context.Context, consumer, eventID string) (bool, error) {
	if !domain.ValidUUID(eventID) {
		return false, fmt.Errorf("%w: event id %q is not a UUID", domain.ErrInvalidArgument, eventID)
	}
	return outbox.ProcessOnce(ctx, t.tx, consumer, eventID)
}

// notificationColumns is the select list scanNotification reads.
var notificationColumns = columnsOf("")

// columnsOf returns the notification select list with each column
// qualified by alias ("n." or "").
func columnsOf(alias string) string {
	return strings.NewReplacer("$.", alias).Replace(`$.id::text, $.recipient_id::text, $.kind, $.title, $.body,
coalesce($.note_id::text, ''), $.block_id, $.range_start, $.range_end, coalesce($.actor_id::text, ''),
$.source_kind, $.source_id, $.coalesce_key, $.occurrences, $.in_inbox, $.created_at, $.updated_at, $.read_at`)
}

func scanNotification(row pgx.Row) (domain.Notification, error) {
	var n domain.Notification
	var kind int16
	var start, end, occurrences int32
	var readAt *time.Time
	var source string
	err := row.Scan(&n.ID, &n.RecipientID, &kind, &n.Title, &n.Body, &n.Location.NoteID, &n.Location.BlockID,
		&start, &end, &n.ActorID, &source, &n.Source.ID, &n.CoalesceKey, &occurrences,
		&n.InInbox, &n.CreatedAt, &n.UpdatedAt, &readAt)
	if err != nil {
		return n, err
	}
	n.Kind, n.Source.Kind = domain.Kind(kind), domain.SourceKind(source)
	n.Location.Start, n.Location.End, n.Occurrences = uint32(start), uint32(end), uint32(occurrences)
	n.CreatedAt, n.UpdatedAt = n.CreatedAt.UTC(), n.UpdatedAt.UTC()
	if readAt != nil {
		n.ReadAt = readAt.UTC()
	}
	return n, nil
}

func collectNotifications(rows pgx.Rows) ([]domain.Notification, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Notification, error) { return scanNotification(row) })
}

func (t *txn) InsertNotification(ctx context.Context, n domain.Notification) error {
	_, err := t.tx.Exec(ctx, `
INSERT INTO notifications (id, recipient_id, kind, title, body, note_id, block_id, range_start, range_end, actor_id,
  source_kind, source_id, coalesce_key, occurrences, in_inbox, created_at, updated_at, read_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		n.ID, n.RecipientID, int16(n.Kind), n.Title, n.Body, nullable(n.Location.NoteID), n.Location.BlockID,
		int32(n.Location.Start), int32(n.Location.End), nullable(n.ActorID), string(n.Source.Kind), n.Source.ID,
		n.CoalesceKey, int32(n.Occurrences), n.InInbox, n.CreatedAt, n.UpdatedAt, nullableTime(n.ReadAt))
	if uniqueViolation(err) {
		return fmt.Errorf("%w: %v", app.ErrConflict, err)
	}
	if err != nil {
		return fmt.Errorf("postgres: insert notification: %w", err)
	}
	return nil
}

func (t *txn) UpdateNotification(ctx context.Context, n domain.Notification) error {
	tag, err := t.tx.Exec(ctx, `
UPDATE notifications SET kind = $2, title = $3, body = $4, note_id = $5, block_id = $6, range_start = $7, range_end = $8,
  actor_id = $9, coalesce_key = $10, occurrences = $11, in_inbox = $12, created_at = $13, updated_at = $14, read_at = $15
WHERE id = $1 AND recipient_id = $16 AND source_kind = $17 AND source_id = $18`,
		n.ID, int16(n.Kind), n.Title, n.Body, nullable(n.Location.NoteID), n.Location.BlockID,
		int32(n.Location.Start), int32(n.Location.End), nullable(n.ActorID), n.CoalesceKey, int32(n.Occurrences),
		n.InInbox, n.CreatedAt, n.UpdatedAt, nullableTime(n.ReadAt), n.RecipientID, string(n.Source.Kind), n.Source.ID)
	if err != nil {
		return fmt.Errorf("postgres: update notification: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("postgres: update matched %d notifications for %s", tag.RowsAffected(), n.ID)
	}
	return nil
}

func (t *txn) one(ctx context.Context, sql string, args ...any) (domain.Notification, bool, error) {
	n, err := scanNotification(t.tx.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Notification{}, false, nil
	}
	if err != nil {
		return n, false, fmt.Errorf("postgres: read notification: %w", err)
	}
	return n, true, nil
}

func (t *txn) NotificationBySource(ctx context.Context, recipientID string, source domain.Source) (domain.Notification, bool, error) {
	return t.one(ctx, `SELECT `+notificationColumns+` FROM notifications
WHERE recipient_id = $1 AND source_kind = $2 AND source_id = $3`, recipientID, string(source.Kind), source.ID)
}

func (t *txn) LatestCoalescible(ctx context.Context, recipientID, key string, since time.Time) (domain.Notification, bool, error) {
	return t.one(ctx, `SELECT `+notificationColumns+` FROM notifications
WHERE recipient_id = $1 AND coalesce_key = $2 AND coalesce_key <> '' AND read_at IS NULL AND created_at >= $3
ORDER BY created_at DESC, id DESC LIMIT 1`, recipientID, key, since)
}

func (t *txn) ListInbox(ctx context.Context, q app.InboxQuery) ([]domain.Notification, error) {
	sql := `SELECT ` + notificationColumns + ` FROM notifications WHERE recipient_id = $1 AND in_inbox`
	args := []any{q.RecipientID}
	if q.UnreadOnly {
		sql += ` AND read_at IS NULL`
	}
	if q.After != nil {
		sql += ` AND (created_at, id) < ($2, $3::uuid)`
		args = append(args, q.After.CreatedAt, q.After.ID)
	}
	sql += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %d`, q.Limit)
	rows, err := t.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("postgres: list inbox: %w", err)
	}
	out, err := collectNotifications(rows)
	if err != nil {
		return nil, fmt.Errorf("postgres: list inbox: %w", err)
	}
	return out, nil
}

func (t *txn) CountUnread(ctx context.Context, recipientID string) (uint32, error) {
	var n int64
	err := t.tx.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_id = $1 AND in_inbox AND read_at IS NULL`,
		recipientID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("postgres: count unread: %w", err)
	}
	return uint32(n), nil
}

func (t *txn) MarkRead(ctx context.Context, recipientID string, ids []string, at time.Time) (int, error) {
	tag, err := t.tx.Exec(ctx, `UPDATE notifications SET read_at = greatest($3, created_at)
WHERE recipient_id = $1 AND id = ANY($2::uuid[]) AND read_at IS NULL`, recipientID, ids, at)
	if err != nil {
		return 0, fmt.Errorf("postgres: mark read: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (t *txn) Preferences(ctx context.Context, userID string) ([]domain.Preference, error) {
	rows, err := t.tx.Query(ctx, `SELECT kind, in_app, email FROM preferences WHERE user_id = $1 ORDER BY kind`, userID)
	if err != nil {
		return nil, fmt.Errorf("postgres: preferences: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.Preference, error) {
		var p domain.Preference
		var kind int16
		err := row.Scan(&kind, &p.InApp, &p.Email)
		p.Kind = domain.Kind(kind)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: preferences: %w", err)
	}
	return out, nil
}

func (t *txn) SavePreferences(ctx context.Context, userID string, prefs []domain.Preference, at time.Time) error {
	for _, p := range prefs {
		_, err := t.tx.Exec(ctx, `
INSERT INTO preferences (user_id, kind, in_app, email, updated_at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, kind) DO UPDATE SET in_app = excluded.in_app, email = excluded.email, updated_at = excluded.updated_at`,
			userID, int16(p.Kind), p.InApp, p.Email, at)
		if err != nil {
			return fmt.Errorf("postgres: save preferences: %w", err)
		}
	}
	return nil
}

func (t *txn) Note(ctx context.Context, noteID string) (app.NoteInfo, bool, error) {
	n := app.NoteInfo{ID: noteID}
	err := t.tx.QueryRow(ctx, `SELECT coalesce(workspace_id::text, ''), title, coalesce(created_by::text, ''), trashed
FROM notes WHERE id = $1`, noteID).Scan(&n.WorkspaceID, &n.Title, &n.CreatedBy, &n.Trashed)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.NoteInfo{}, false, nil
	}
	if err != nil {
		return app.NoteInfo{}, false, fmt.Errorf("postgres: note: %w", err)
	}
	return n, true, nil
}

func (t *txn) SaveNote(ctx context.Context, n app.NoteInfo) error {
	_, err := t.tx.Exec(ctx, `
INSERT INTO notes (id, workspace_id, title, created_by, trashed) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO UPDATE SET workspace_id = excluded.workspace_id, title = excluded.title,
  created_by = excluded.created_by, trashed = excluded.trashed`,
		n.ID, nullable(n.WorkspaceID), n.Title, nullable(n.CreatedBy), n.Trashed)
	if err != nil {
		return fmt.Errorf("postgres: save note: %w", err)
	}
	return nil
}

func (t *txn) BlockMentions(ctx context.Context, noteID string, blockIDs []string) (map[string][]string, error) {
	rows, err := t.tx.Query(ctx, `SELECT block_id, user_id::text FROM block_mentions
WHERE note_id = $1 AND block_id = ANY($2) ORDER BY block_id, user_id`, noteID, blockIDs)
	if err != nil {
		return nil, fmt.Errorf("postgres: block mentions: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var block, user string
		if err := rows.Scan(&block, &user); err != nil {
			return nil, fmt.Errorf("postgres: block mentions: %w", err)
		}
		out[block] = append(out[block], user)
	}
	return out, rows.Err()
}

func (t *txn) SetBlockMentions(ctx context.Context, noteID, blockID string, users []string) error {
	if _, err := t.tx.Exec(ctx, `DELETE FROM block_mentions WHERE note_id = $1 AND block_id = $2`, noteID, blockID); err != nil {
		return fmt.Errorf("postgres: clear block mentions: %w", err)
	}
	if len(users) == 0 {
		return nil
	}
	_, err := t.tx.Exec(ctx, `INSERT INTO block_mentions (note_id, block_id, user_id)
SELECT $1, $2, u FROM unnest($3::uuid[]) AS u`, noteID, blockID, users)
	if err != nil {
		return fmt.Errorf("postgres: set block mentions: %w", err)
	}
	return nil
}

func (t *txn) PurgeNote(ctx context.Context, noteID string) ([]string, error) {
	rows, err := t.tx.Query(ctx, `DELETE FROM notifications WHERE note_id = $1 RETURNING recipient_id::text, in_inbox`, noteID)
	if err != nil {
		return nil, fmt.Errorf("postgres: purge notifications: %w", err)
	}
	seen := map[string]bool{}
	var recipients []string
	for rows.Next() {
		var user string
		var inInbox bool
		if err := rows.Scan(&user, &inInbox); err != nil {
			rows.Close()
			return nil, fmt.Errorf("postgres: purge notifications: %w", err)
		}
		if inInbox && !seen[user] {
			seen[user] = true
			recipients = append(recipients, user)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: purge notifications: %w", err)
	}
	for _, sql := range []string{`DELETE FROM block_mentions WHERE note_id = $1`, `DELETE FROM notes WHERE id = $1`} {
		if _, err := t.tx.Exec(ctx, sql, noteID); err != nil {
			return nil, fmt.Errorf("postgres: purge note: %w", err)
		}
	}
	return recipients, nil
}
