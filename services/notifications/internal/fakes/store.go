// Package fakes holds in-memory implementations of the app ports, for unit
// tests and the seed loader's rule check. They implement the same contracts
// as the real adapters (the Postgres store, identity, SMTP, Redis), and
// expose CheckInvariants so tests can verify state after every operation.
//
// Store runs one transaction at a time on a copy of its state and swaps the
// copy in on commit, so a failed or rolled-back transaction leaves nothing
// behind, which is what the producer's and the email sender's crash tests
// depend on. It is not fast (each transaction copies everything) and does
// not need to be.
package fakes

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

// ErrInjected is the error an injected commit failure returns.
var ErrInjected = errors.New("fakes: injected failure")

// Job states in the email outbox.
const (
	JobPending  = "pending"
	JobAssigned = "assigned"
	JobSkipped  = "skipped"
)

// JobRow is an email job as stored.
type JobRow struct {
	app.EmailJob
	State  string
	SendID string
}

type sourceKey struct {
	recipient string
	source    domain.Source
}

type state struct {
	notifications map[string]domain.Notification
	bySource      map[sourceKey]string
	processed     map[string]bool
	prefs         map[string]map[domain.Kind]domain.Preference
	notes         map[string]app.NoteInfo
	mentions      map[string]map[string][]string
	jobs          map[string]JobRow
	sends         map[string]app.EmailSend
}

func newState() *state {
	return &state{
		notifications: map[string]domain.Notification{}, bySource: map[sourceKey]string{}, processed: map[string]bool{},
		prefs: map[string]map[domain.Kind]domain.Preference{}, notes: map[string]app.NoteInfo{},
		mentions: map[string]map[string][]string{}, jobs: map[string]JobRow{}, sends: map[string]app.EmailSend{},
	}
}

func (s *state) clone() *state {
	c := &state{
		notifications: maps.Clone(s.notifications), bySource: maps.Clone(s.bySource), processed: maps.Clone(s.processed),
		prefs: map[string]map[domain.Kind]domain.Preference{}, notes: maps.Clone(s.notes),
		mentions: map[string]map[string][]string{}, jobs: maps.Clone(s.jobs), sends: maps.Clone(s.sends),
	}
	for u, p := range s.prefs {
		c.prefs[u] = maps.Clone(p)
	}
	for n, blocks := range s.mentions {
		c.mentions[n] = map[string][]string{}
		for b, users := range blocks {
			c.mentions[n][b] = slices.Clone(users)
		}
	}
	return c
}

// Store is an in-memory app.Store.
type Store struct {
	mu    sync.Mutex
	state *state
	// failCommits makes the next n commits fail with ErrInjected.
	failCommits int
	commits     int
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{state: newState()} }

// FailNextCommits makes the next n commits fail, as if the process died or
// lost the database before COMMIT returned.
func (s *Store) FailNextCommits(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failCommits = n
}

// Commits returns how many transactions have committed.
func (s *Store) Commits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits
}

// WithTx implements app.Store.
func (s *Store) WithTx(ctx context.Context, fn func(ctx context.Context, tx app.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	work := s.state.clone()
	if err := fn(ctx, &tx{s: work}); err != nil {
		return err
	}
	if s.failCommits > 0 {
		s.failCommits--
		return ErrInjected
	}
	s.state = work
	s.commits++
	return nil
}

// Notifications returns every stored notification, ordered by ID.
func (s *Store) Notifications() []domain.Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.SortedFunc(maps.Values(s.state.notifications), func(a, b domain.Notification) int { return cmp.Compare(a.ID, b.ID) })
}

// Sends returns every email send, ordered by ID, without notifications.
func (s *Store) Sends() []app.EmailSend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.SortedFunc(maps.Values(s.state.sends), func(a, b app.EmailSend) int { return cmp.Compare(a.ID, b.ID) })
}

// Jobs returns every email job, ordered by ID.
func (s *Store) Jobs() []JobRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.SortedFunc(maps.Values(s.state.jobs), func(a, b JobRow) int { return cmp.Compare(a.ID, b.ID) })
}

// CheckInvariants verifies the store's internal consistency.
func (s *Store) CheckInvariants() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state
	for id, n := range st.notifications {
		if n.ID != id {
			return fmt.Errorf("store: notification keyed %s has id %s", id, n.ID)
		}
		if err := n.Validate(); err != nil {
			return fmt.Errorf("store: notification %s: %w", id, err)
		}
		if st.bySource[sourceKey{n.RecipientID, n.Source}] != id {
			return fmt.Errorf("store: source index misses notification %s", id)
		}
	}
	if len(st.bySource) != len(st.notifications) {
		return fmt.Errorf("store: source index has %d entries for %d notifications", len(st.bySource), len(st.notifications))
	}
	unassigned := map[string]bool{}
	for id, j := range st.jobs {
		n, ok := st.notifications[j.NotificationID]
		switch {
		case !ok:
			return fmt.Errorf("store: job %s outlived its notification", id)
		case n.RecipientID != j.RecipientID:
			return fmt.Errorf("store: job %s is for another recipient than its notification", id)
		case j.State == JobPending && j.SendID != "":
			return fmt.Errorf("store: pending job %s has a send", id)
		case j.State == JobPending && unassigned[j.NotificationID]:
			return fmt.Errorf("store: notification %s has two unassigned jobs", j.NotificationID)
		case j.State == JobAssigned:
			send, ok := st.sends[j.SendID]
			if !ok || send.RecipientID != j.RecipientID {
				return fmt.Errorf("store: job %s assigned to a missing or foreign send", id)
			}
		}
		if j.State == JobPending {
			unassigned[j.NotificationID] = true
		}
	}
	for id, send := range st.sends {
		switch send.State {
		case app.EmailPending, app.EmailDead:
		case app.EmailSending:
			if send.LeaseUntil.IsZero() || send.Attempts == 0 {
				return fmt.Errorf("store: sending %s without lease or attempt", id)
			}
		case app.EmailSent:
			if send.SentAt.IsZero() {
				return fmt.Errorf("store: sent %s without a time", id)
			}
		default:
			return fmt.Errorf("store: send %s in state %q", id, send.State)
		}
	}
	for note, blocks := range st.mentions {
		for b, users := range blocks {
			if len(users) == 0 || !slices.IsSorted(users) || len(slices.Compact(slices.Clone(users))) != len(users) {
				return fmt.Errorf("store: mentions of %s/%s not sorted, distinct and non-empty", note, b)
			}
		}
	}
	return nil
}

type tx struct{ s *state }

func (t *tx) MarkProcessed(_ context.Context, consumer, eventID string) (bool, error) {
	key := consumer + "\x00" + eventID
	if t.s.processed[key] {
		return false, nil
	}
	t.s.processed[key] = true
	return true, nil
}

func (t *tx) InsertNotification(_ context.Context, n domain.Notification) error {
	key := sourceKey{n.RecipientID, n.Source}
	if _, ok := t.s.notifications[n.ID]; ok {
		return fmt.Errorf("%w: notification %s", app.ErrConflict, n.ID)
	}
	if _, ok := t.s.bySource[key]; ok {
		return fmt.Errorf("%w: source %v", app.ErrConflict, n.Source)
	}
	t.s.notifications[n.ID] = n
	t.s.bySource[key] = n.ID
	return nil
}

func (t *tx) UpdateNotification(_ context.Context, n domain.Notification) error {
	old, ok := t.s.notifications[n.ID]
	if !ok {
		return fmt.Errorf("fakes: update of unknown notification %s", n.ID)
	}
	if old.RecipientID != n.RecipientID || old.Source != n.Source {
		return fmt.Errorf("fakes: update may not change recipient or source")
	}
	t.s.notifications[n.ID] = n
	return nil
}

func (t *tx) NotificationBySource(_ context.Context, recipientID string, source domain.Source) (domain.Notification, bool, error) {
	id, ok := t.s.bySource[sourceKey{recipientID, source}]
	if !ok {
		return domain.Notification{}, false, nil
	}
	return t.s.notifications[id], true, nil
}

// newestFirst orders by (CreatedAt, ID) descending, the inbox order.
func newestFirst(a, b domain.Notification) int {
	return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
}

func (t *tx) LatestCoalescible(_ context.Context, recipientID, key string, since time.Time) (domain.Notification, bool, error) {
	var best domain.Notification
	found := false
	for _, n := range t.s.notifications {
		if n.RecipientID != recipientID || n.CoalesceKey != key || n.Read() || n.CreatedAt.Before(since) {
			continue
		}
		if !found || newestFirst(n, best) < 0 {
			best, found = n, true
		}
	}
	return best, found, nil
}

func (t *tx) ListInbox(_ context.Context, q app.InboxQuery) ([]domain.Notification, error) {
	var out []domain.Notification
	for _, n := range t.s.notifications {
		if n.RecipientID != q.RecipientID || !n.InInbox || (q.UnreadOnly && n.Read()) || (q.After != nil && !q.After.After(n)) {
			continue
		}
		out = append(out, n)
	}
	slices.SortFunc(out, newestFirst)
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (t *tx) CountUnread(_ context.Context, recipientID string) (uint32, error) {
	var n uint32
	for _, x := range t.s.notifications {
		if x.RecipientID == recipientID && x.InInbox && !x.Read() {
			n++
		}
	}
	return n, nil
}

func (t *tx) MarkRead(_ context.Context, recipientID string, ids []string, at time.Time) (int, error) {
	changed := 0
	for _, id := range ids {
		n, ok := t.s.notifications[id]
		if !ok || n.RecipientID != recipientID || n.Read() {
			continue
		}
		n.ReadAt = at
		if at.Before(n.CreatedAt) {
			n.ReadAt = n.CreatedAt
		}
		t.s.notifications[id] = n
		changed++
	}
	return changed, nil
}

func (t *tx) Preferences(_ context.Context, userID string) ([]domain.Preference, error) {
	var out []domain.Preference
	for _, k := range domain.Kinds {
		if p, ok := t.s.prefs[userID][k]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (t *tx) SavePreferences(_ context.Context, userID string, prefs []domain.Preference, _ time.Time) error {
	if t.s.prefs[userID] == nil {
		t.s.prefs[userID] = map[domain.Kind]domain.Preference{}
	}
	for _, p := range prefs {
		t.s.prefs[userID][p.Kind] = p
	}
	return nil
}

func (t *tx) Note(_ context.Context, noteID string) (app.NoteInfo, bool, error) {
	n, ok := t.s.notes[noteID]
	return n, ok, nil
}

func (t *tx) SaveNote(_ context.Context, note app.NoteInfo) error {
	t.s.notes[note.ID] = note
	return nil
}

func (t *tx) BlockMentions(_ context.Context, noteID string, blockIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, b := range blockIDs {
		if users, ok := t.s.mentions[noteID][b]; ok {
			out[b] = slices.Clone(users)
		}
	}
	return out, nil
}

func (t *tx) SetBlockMentions(_ context.Context, noteID, blockID string, users []string) error {
	if len(users) == 0 {
		delete(t.s.mentions[noteID], blockID)
		if len(t.s.mentions[noteID]) == 0 {
			delete(t.s.mentions, noteID)
		}
		return nil
	}
	if t.s.mentions[noteID] == nil {
		t.s.mentions[noteID] = map[string][]string{}
	}
	t.s.mentions[noteID][blockID] = slices.Clone(users)
	return nil
}

func (t *tx) PurgeNote(_ context.Context, noteID string) ([]string, error) {
	var recipients []string
	for id, n := range t.s.notifications {
		if n.Location.NoteID != noteID {
			continue
		}
		delete(t.s.notifications, id)
		delete(t.s.bySource, sourceKey{n.RecipientID, n.Source})
		if n.InInbox {
			recipients = append(recipients, n.RecipientID)
		}
		for jid, j := range t.s.jobs {
			if j.NotificationID == id {
				delete(t.s.jobs, jid)
			}
		}
	}
	delete(t.s.mentions, noteID)
	delete(t.s.notes, noteID)
	slices.Sort(recipients)
	return slices.Compact(recipients), nil
}

func (t *tx) EnqueueEmail(_ context.Context, job app.EmailJob) error {
	for _, j := range t.s.jobs {
		if j.NotificationID == job.NotificationID && j.State == JobPending {
			return nil
		}
	}
	if _, ok := t.s.notifications[job.NotificationID]; !ok {
		return fmt.Errorf("fakes: email job for unknown notification %s", job.NotificationID)
	}
	t.s.jobs[job.ID] = JobRow{EmailJob: job, State: JobPending}
	return nil
}

func (t *tx) PendingEmails(_ context.Context, now time.Time, recipientLimit int) ([]domain.PendingEmail, error) {
	earliest := map[string]time.Time{}
	for _, j := range t.s.jobs {
		if j.State != JobPending || j.DueAt.After(now) {
			continue
		}
		if e, ok := earliest[j.RecipientID]; !ok || j.DueAt.Before(e) {
			earliest[j.RecipientID] = j.DueAt
		}
	}
	recipients := slices.SortedFunc(maps.Keys(earliest), func(a, b string) int {
		return cmp.Or(earliest[a].Compare(earliest[b]), cmp.Compare(a, b))
	})
	if len(recipients) > recipientLimit {
		recipients = recipients[:recipientLimit]
	}
	var out []domain.PendingEmail
	for _, j := range t.s.jobs {
		if j.State != JobPending || !slices.Contains(recipients, j.RecipientID) {
			continue
		}
		out = append(out, domain.PendingEmail{JobID: j.ID, RecipientID: j.RecipientID, NotificationID: j.NotificationID,
			CreatedAt: j.CreatedAt, DueAt: j.DueAt, Read: t.s.notifications[j.NotificationID].Read()})
	}
	slices.SortFunc(out, func(a, b domain.PendingEmail) int { return cmp.Compare(a.JobID, b.JobID) })
	return out, nil
}

func (t *tx) CreateEmailSend(_ context.Context, send app.EmailSend, jobIDs []string) error {
	if _, ok := t.s.sends[send.ID]; ok {
		return fmt.Errorf("%w: send %s", app.ErrConflict, send.ID)
	}
	for _, id := range jobIDs {
		j, ok := t.s.jobs[id]
		if !ok || j.State != JobPending || j.RecipientID != send.RecipientID {
			return fmt.Errorf("fakes: job %s cannot join send %s", id, send.ID)
		}
		j.State, j.SendID = JobAssigned, send.ID
		t.s.jobs[id] = j
	}
	send.State = app.EmailPending
	send.Notifications = nil
	t.s.sends[send.ID] = send
	return nil
}

func (t *tx) SkipEmailJobs(_ context.Context, jobIDs []string) error {
	for _, id := range jobIDs {
		if j, ok := t.s.jobs[id]; ok && j.State == JobPending {
			j.State = JobSkipped
			t.s.jobs[id] = j
		}
	}
	return nil
}

func (t *tx) ClaimEmailSends(_ context.Context, now time.Time, lease time.Duration, limit int) ([]app.EmailSend, error) {
	var due []app.EmailSend
	for _, s := range t.s.sends {
		if (s.State == app.EmailPending && !s.NextAttemptAt.After(now)) || (s.State == app.EmailSending && !s.LeaseUntil.After(now)) {
			due = append(due, s)
		}
	}
	slices.SortFunc(due, func(a, b app.EmailSend) int {
		return cmp.Or(a.NextAttemptAt.Compare(b.NextAttemptAt), cmp.Compare(a.ID, b.ID))
	})
	if len(due) > limit {
		due = due[:limit]
	}
	for i, s := range due {
		s.State, s.Attempts, s.LeaseUntil = app.EmailSending, s.Attempts+1, now.Add(lease)
		t.s.sends[s.ID] = s
		for _, j := range t.s.jobs {
			if j.SendID == s.ID && j.State == JobAssigned {
				if n, ok := t.s.notifications[j.NotificationID]; ok {
					s.Notifications = append(s.Notifications, n)
				}
			}
		}
		slices.SortFunc(s.Notifications, func(a, b domain.Notification) int {
			return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
		})
		due[i] = s
	}
	return due, nil
}

func (t *tx) FinishEmailSend(_ context.Context, sendID string, attempt int, outcome app.EmailOutcome) error {
	s, ok := t.s.sends[sendID]
	current := ok && s.State == app.EmailSending && s.Attempts == attempt
	lateSuccess := ok && outcome.State == app.EmailSent && s.State != app.EmailSent
	if !current && !lateSuccess {
		return nil
	}
	s.LastError = outcome.Error
	s.LeaseUntil = time.Time{}
	switch outcome.State {
	case app.EmailSent:
		s.State, s.SentAt = app.EmailSent, outcome.At
	case app.EmailPending:
		s.State, s.NextAttemptAt = app.EmailPending, outcome.RetryAt
	case app.EmailDead:
		s.State = app.EmailDead
	default:
		return fmt.Errorf("fakes: finish with state %q", outcome.State)
	}
	t.s.sends[sendID] = s
	return nil
}
