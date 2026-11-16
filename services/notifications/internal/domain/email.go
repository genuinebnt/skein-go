package domain

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// PendingEmail is an email job not yet assigned to a send.
type PendingEmail struct {
	JobID          string
	RecipientID    string
	NotificationID string
	CreatedAt      time.Time
	// DueAt is when the job may go out at the earliest: its creation plus
	// the digest delay, which gives a burst time to accumulate.
	DueAt time.Time
	// Read reports whether the user has already read the notification in
	// the app; such jobs are skipped instead of sent.
	Read bool
}

// EmailBatch is the jobs one email carries: a single notification, or a
// digest of several, always for one recipient.
type EmailBatch struct {
	RecipientID string
	Jobs        []PendingEmail
}

// EmailPlan is what PlanEmails decided.
type EmailPlan struct {
	Batches []EmailBatch
	// Skipped are jobs whose notification was read before the email left.
	Skipped []string
}

// PlanEmails groups pending jobs into emails. A recipient is due when any
// of their unread jobs is due; then all of their unread pending jobs go
// out together, oldest first, in emails of at most maxPerEmail
// notifications (a batch of one is a plain email, more is a digest). Read
// jobs are skipped whatever their due time. Output is ordered by recipient
// ID, then by job order, so the plan is deterministic.
func PlanEmails(pending []PendingEmail, now time.Time, maxPerEmail int) EmailPlan {
	maxPerEmail = max(1, maxPerEmail)
	byRecipient := map[string][]PendingEmail{}
	var recipients []string
	var plan EmailPlan
	for _, p := range pending {
		if p.Read {
			plan.Skipped = append(plan.Skipped, p.JobID)
			continue
		}
		if _, ok := byRecipient[p.RecipientID]; !ok {
			recipients = append(recipients, p.RecipientID)
		}
		byRecipient[p.RecipientID] = append(byRecipient[p.RecipientID], p)
	}
	slices.Sort(plan.Skipped)
	slices.Sort(recipients)
	for _, r := range recipients {
		jobs := byRecipient[r]
		if !slices.ContainsFunc(jobs, func(p PendingEmail) bool { return !p.DueAt.After(now) }) {
			continue
		}
		slices.SortFunc(jobs, func(a, b PendingEmail) int {
			return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.JobID, b.JobID))
		})
		for chunk := range slices.Chunk(jobs, maxPerEmail) {
			plan.Batches = append(plan.Batches, EmailBatch{RecipientID: r, Jobs: chunk})
		}
	}
	return plan
}

// CheckInvariants verifies a plan against its input: every job appears at
// most once across batches and skips, each batch is non-empty, within the
// size limit, for one recipient, and has at least one due job unless an
// earlier batch of the same recipient does; skipped jobs are exactly the
// read ones.
func (p EmailPlan) CheckInvariants(pending []PendingEmail, now time.Time, maxPerEmail int) error {
	byID := map[string]PendingEmail{}
	for _, j := range pending {
		byID[j.JobID] = j
	}
	used := map[string]bool{}
	dueRecipient := map[string]bool{}
	for i, b := range p.Batches {
		if len(b.Jobs) == 0 || len(b.Jobs) > max(1, maxPerEmail) {
			return fmt.Errorf("email plan: batch %d has %d jobs", i, len(b.Jobs))
		}
		for _, j := range b.Jobs {
			src, ok := byID[j.JobID]
			switch {
			case !ok:
				return fmt.Errorf("email plan: batch %d holds unknown job %s", i, j.JobID)
			case used[j.JobID]:
				return fmt.Errorf("email plan: job %s planned twice", j.JobID)
			case src.RecipientID != b.RecipientID:
				return fmt.Errorf("email plan: job %s is for another recipient", j.JobID)
			case src.Read:
				return fmt.Errorf("email plan: read job %s planned", j.JobID)
			}
			used[j.JobID] = true
			if !src.DueAt.After(now) {
				dueRecipient[b.RecipientID] = true
			}
		}
	}
	for _, b := range p.Batches {
		if !dueRecipient[b.RecipientID] {
			return fmt.Errorf("email plan: recipient %s planned with nothing due", b.RecipientID)
		}
	}
	for _, id := range p.Skipped {
		if used[id] || !byID[id].Read {
			return fmt.Errorf("email plan: job %s wrongly skipped", id)
		}
		used[id] = true
	}
	for _, j := range pending {
		if j.Read && !used[j.JobID] {
			return fmt.Errorf("email plan: read job %s neither skipped nor planned", j.JobID)
		}
		if !j.Read && dueRecipient[j.RecipientID] && !used[j.JobID] {
			return fmt.Errorf("email plan: job %s of a due recipient left behind", j.JobID)
		}
	}
	return nil
}

// RetryDelay is the wait before retry number attempt (1 for the first
// retry): initial doubled per attempt, capped at maximum. Callers add
// jitter.
func RetryDelay(attempt int, initial, maximum time.Duration) time.Duration {
	d := initial
	for i := 1; i < attempt && d < maximum; i++ {
		d *= 2
	}
	return min(d, maximum)
}
