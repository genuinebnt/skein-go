package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

func (t *txn) EnqueueEmail(ctx context.Context, job app.EmailJob) error {
	_, err := t.tx.Exec(ctx, `
INSERT INTO email_jobs (id, recipient_id, notification_id, state, created_at, due_at)
VALUES ($1, $2, $3, 'pending', $4, $5)
ON CONFLICT (notification_id) WHERE state = 'pending' DO NOTHING`,
		job.ID, job.RecipientID, job.NotificationID, job.CreatedAt, job.DueAt)
	if err != nil {
		return fmt.Errorf("postgres: enqueue email: %w", err)
	}
	return nil
}

func (t *txn) PendingEmails(ctx context.Context, now time.Time, recipientLimit int) ([]domain.PendingEmail, error) {
	rows, err := t.tx.Query(ctx, `
WITH due AS (
  SELECT recipient_id, min(due_at) AS first_due FROM email_jobs
  WHERE state = 'pending' AND due_at <= $1
  GROUP BY recipient_id ORDER BY first_due, recipient_id LIMIT $2
)
SELECT j.id::text, j.recipient_id::text, j.notification_id::text, j.created_at, j.due_at, n.read_at IS NOT NULL
FROM email_jobs j JOIN due USING (recipient_id) JOIN notifications n ON n.id = j.notification_id
WHERE j.state = 'pending'
ORDER BY j.id
FOR UPDATE OF j SKIP LOCKED`, now, recipientLimit)
	if err != nil {
		return nil, fmt.Errorf("postgres: pending emails: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.PendingEmail, error) {
		var p domain.PendingEmail
		err := row.Scan(&p.JobID, &p.RecipientID, &p.NotificationID, &p.CreatedAt, &p.DueAt, &p.Read)
		p.CreatedAt, p.DueAt = p.CreatedAt.UTC(), p.DueAt.UTC()
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: pending emails: %w", err)
	}
	return out, nil
}

func (t *txn) CreateEmailSend(ctx context.Context, send app.EmailSend, jobIDs []string) error {
	_, err := t.tx.Exec(ctx, `
INSERT INTO email_sends (id, recipient_id, state, attempts, next_attempt_at, created_at)
VALUES ($1, $2, 'pending', 0, $3, $4)`, send.ID, send.RecipientID, send.NextAttemptAt, send.CreatedAt)
	if uniqueViolation(err) {
		return fmt.Errorf("%w: send %s", app.ErrConflict, send.ID)
	}
	if err != nil {
		return fmt.Errorf("postgres: create email send: %w", err)
	}
	tag, err := t.tx.Exec(ctx, `UPDATE email_jobs SET state = 'assigned', send_id = $1
WHERE id = ANY($2::uuid[]) AND state = 'pending' AND recipient_id = $3`, send.ID, jobIDs, send.RecipientID)
	if err != nil {
		return fmt.Errorf("postgres: assign email jobs: %w", err)
	}
	if int(tag.RowsAffected()) != len(jobIDs) {
		// PendingEmails locked the rows, so this means a caller bug.
		return fmt.Errorf("postgres: assigned %d of %d email jobs to %s", tag.RowsAffected(), len(jobIDs), send.ID)
	}
	return nil
}

func (t *txn) SkipEmailJobs(ctx context.Context, jobIDs []string) error {
	_, err := t.tx.Exec(ctx, `UPDATE email_jobs SET state = 'skipped' WHERE id = ANY($1::uuid[]) AND state = 'pending'`, jobIDs)
	if err != nil {
		return fmt.Errorf("postgres: skip email jobs: %w", err)
	}
	return nil
}

func (t *txn) ClaimEmailSends(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]app.EmailSend, error) {
	rows, err := t.tx.Query(ctx, `
WITH due AS (
  SELECT id FROM email_sends
  WHERE (state = 'pending' AND next_attempt_at <= $1) OR (state = 'sending' AND lease_until <= $1)
  ORDER BY next_attempt_at, id
  LIMIT $3
  FOR UPDATE SKIP LOCKED
)
UPDATE email_sends s SET state = 'sending', attempts = s.attempts + 1, lease_until = $2
FROM due WHERE s.id = due.id
RETURNING s.id::text, s.recipient_id::text, s.attempts, s.next_attempt_at, s.lease_until, s.last_error, s.created_at`,
		now, now.Add(lease), limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: claim email sends: %w", err)
	}
	sends, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (app.EmailSend, error) {
		s := app.EmailSend{State: app.EmailSending}
		var attempts int32
		err := row.Scan(&s.ID, &s.RecipientID, &attempts, &s.NextAttemptAt, &s.LeaseUntil, &s.LastError, &s.CreatedAt)
		s.Attempts = int(attempts)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: claim email sends: %w", err)
	}
	if len(sends) == 0 {
		return nil, nil
	}
	ids := make([]string, len(sends))
	index := map[string]int{}
	for i, s := range sends {
		ids[i], index[s.ID] = s.ID, i
	}
	rows, err = t.tx.Query(ctx, `SELECT j.send_id::text, `+columnsOf("n.")+`
FROM email_jobs j JOIN notifications n ON n.id = j.notification_id
WHERE j.send_id = ANY($1::uuid[]) AND j.state = 'assigned'
ORDER BY j.send_id, n.created_at, n.id`, ids)
	if err != nil {
		return nil, fmt.Errorf("postgres: email send notifications: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sendID string
		n, err := scanNotification(prefixedRow{rows, &sendID})
		if err != nil {
			return nil, fmt.Errorf("postgres: email send notifications: %w", err)
		}
		i := index[sendID]
		sends[i].Notifications = append(sends[i].Notifications, n)
	}
	return sends, rows.Err()
}

func (t *txn) FinishEmailSend(ctx context.Context, sendID string, attempt int, o app.EmailOutcome) error {
	var err error
	switch o.State {
	case app.EmailSent:
		_, err = t.tx.Exec(ctx, `UPDATE email_sends SET state = 'sent', sent_at = $2, lease_until = NULL, last_error = $3
WHERE id = $1 AND state <> 'sent'`, sendID, o.At, o.Error)
	case app.EmailPending:
		_, err = t.tx.Exec(ctx, `UPDATE email_sends SET state = 'pending', next_attempt_at = $3, lease_until = NULL, last_error = $4
WHERE id = $1 AND state = 'sending' AND attempts = $2`, sendID, attempt, o.RetryAt, o.Error)
	case app.EmailDead:
		_, err = t.tx.Exec(ctx, `UPDATE email_sends SET state = 'dead', lease_until = NULL, last_error = $3
WHERE id = $1 AND state = 'sending' AND attempts = $2`, sendID, attempt, o.Error)
	default:
		return fmt.Errorf("postgres: finish email send with state %q", o.State)
	}
	if err != nil {
		return fmt.Errorf("postgres: finish email send: %w", err)
	}
	return nil
}

// prefixedRow scans a leading send ID, then a notification.
type prefixedRow struct {
	rows   pgx.Rows
	sendID *string
}

func (p prefixedRow) Scan(dest ...any) error {
	return p.rows.Scan(append([]any{p.sendID}, dest...)...)
}
