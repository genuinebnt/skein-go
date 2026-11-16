package app

import (
	"context"
	"errors"
	"time"

	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
	"github.com/genuinebnt/skein-go/services/notifications/internal/email"
)

// Errors the ports return. Test with errors.Is.
var (
	// ErrConflict means an insert hit a unique key: the notification ID, or
	// (recipient, source).
	ErrConflict = errors.New("conflict")
	// ErrUnknownUser means the directory has no such user.
	ErrUnknownUser = errors.New("unknown user")
	// ErrPermanent wraps a delivery failure that retrying cannot fix (an
	// SMTP 5xx reply, a malformed address).
	ErrPermanent = errors.New("permanent failure")
	// ErrForbidden means an event asked for something its publisher may not
	// do, such as an agent run notifying someone other than its user.
	ErrForbidden = errors.New("forbidden")
)

// Store runs transactions over the service's own data. Every use case does
// all its reads and writes for one request or event inside one WithTx, so
// a crash leaves either all of it or none of it.
type Store interface {
	WithTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error
}

// Tx is one transaction. It is split into the three groups below only for
// reading; one implementation provides all of them.
type Tx interface {
	InboxTx
	ProjectionTx
	EmailTx
	// MarkProcessed records that consumer has handled eventID and reports
	// whether this is the first time (the platform's idempotent-consumer
	// table, outbox.ProcessOnce, in Postgres).
	MarkProcessed(ctx context.Context, consumer, eventID string) (bool, error)
}

// InboxQuery selects one page of a user's inbox.
type InboxQuery struct {
	RecipientID string
	UnreadOnly  bool
	// After is the cursor of the previous page, or nil for the first.
	After *domain.InboxCursor
	Limit int
}

// InboxTx reads and writes notifications and preferences.
type InboxTx interface {
	// InsertNotification stores a new notification; ErrConflict when its ID
	// or its (recipient, source) is taken.
	InsertNotification(ctx context.Context, n domain.Notification) error
	// UpdateNotification replaces the stored notification with the same ID.
	UpdateNotification(ctx context.Context, n domain.Notification) error
	NotificationBySource(ctx context.Context, recipientID string, source domain.Source) (domain.Notification, bool, error)
	// LatestCoalescible returns the newest unread notification of the
	// recipient with this coalescing key created at or after since.
	LatestCoalescible(ctx context.Context, recipientID, key string, since time.Time) (domain.Notification, bool, error)
	// ListInbox returns notifications with InInbox set, newest first by
	// (CreatedAt, ID).
	ListInbox(ctx context.Context, q InboxQuery) ([]domain.Notification, error)
	CountUnread(ctx context.Context, recipientID string) (uint32, error)
	// MarkRead sets ReadAt on the recipient's unread notifications among
	// ids and returns how many changed. Other users' IDs are ignored.
	MarkRead(ctx context.Context, recipientID string, ids []string, at time.Time) (int, error)
	// Preferences returns the user's stored overrides, any order.
	Preferences(ctx context.Context, userID string) ([]domain.Preference, error)
	// SavePreferences upserts the listed kinds.
	SavePreferences(ctx context.Context, userID string, prefs []domain.Preference, at time.Time) error
}

// NoteInfo is what the service keeps about a note, projected from
// documents.events: enough to title notifications and resolve mentions.
type NoteInfo struct {
	ID          string
	WorkspaceID string
	Title       string
	// CreatedBy is the actor of NoteCreated.
	CreatedBy string
	Trashed   bool
}

// ProjectionTx reads and writes the note and mention projections.
type ProjectionTx interface {
	Note(ctx context.Context, noteID string) (NoteInfo, bool, error)
	SaveNote(ctx context.Context, note NoteInfo) error
	// BlockMentions returns, for each listed block that has any, the users
	// its text mentions, ascending.
	BlockMentions(ctx context.Context, noteID string, blockIDs []string) (map[string][]string, error)
	// SetBlockMentions replaces a block's mentioned users; an empty list
	// deletes the block's rows.
	SetBlockMentions(ctx context.Context, noteID, blockID string, users []string) error
	// PurgeNote erases everything held about a note: its projection rows,
	// the notifications located in it and their email jobs. It returns the
	// recipients whose inbox changed.
	PurgeNote(ctx context.Context, noteID string) ([]string, error)
}

// EmailJob asks for one notification to be emailed.
type EmailJob struct {
	ID             string
	RecipientID    string
	NotificationID string
	CreatedAt      time.Time
	DueAt          time.Time
}

// EmailSendState is where an email send is in its life.
type EmailSendState string

// Email send states. pending -> sending -> sent | pending (retry) | dead.
const (
	EmailPending EmailSendState = "pending"
	EmailSending EmailSendState = "sending"
	EmailSent    EmailSendState = "sent"
	EmailDead    EmailSendState = "dead"
)

// EmailSend is one email to one recipient carrying one or more jobs (a
// digest when more). Its ID is the email's Message-ID, so a re-send after a
// crash is recognisably the same message.
type EmailSend struct {
	ID            string
	RecipientID   string
	State         EmailSendState
	Attempts      int
	NextAttemptAt time.Time
	LeaseUntil    time.Time
	LastError     string
	CreatedAt     time.Time
	SentAt        time.Time
	// Notifications are the jobs' notifications, oldest first, filled by
	// ClaimEmailSends. Purged notifications are missing.
	Notifications []domain.Notification
}

// EmailOutcome is the result of one send attempt.
type EmailOutcome struct {
	State EmailSendState // EmailSent, EmailPending (retry) or EmailDead
	At    time.Time
	// RetryAt is when a pending send is due again.
	RetryAt time.Time
	Error   string
}

// EmailTx is the email outbox: jobs written in the same transaction as
// their notification, grouped into sends, claimed with a lease.
type EmailTx interface {
	// EnqueueEmail stores a job unless the notification already has an
	// unassigned one (a notification refreshed twice before its email
	// leaves is emailed once).
	EnqueueEmail(ctx context.Context, job EmailJob) error
	// PendingEmails returns every unassigned job of up to recipientLimit
	// recipients that have an unassigned job due at now, with each job's
	// notification read state.
	PendingEmails(ctx context.Context, now time.Time, recipientLimit int) ([]domain.PendingEmail, error)
	// CreateEmailSend stores a pending send due at send.NextAttemptAt and
	// assigns the jobs to it.
	CreateEmailSend(ctx context.Context, send EmailSend, jobIDs []string) error
	// SkipEmailJobs marks unassigned jobs as never to be sent.
	SkipEmailJobs(ctx context.Context, jobIDs []string) error
	// ClaimEmailSends takes up to limit sends that are pending and due, or
	// sending with an expired lease (their sender crashed), sets them to
	// sending with a lease until now+lease and one more attempt, and
	// returns them with their notifications. Concurrent claimers never get
	// the same send.
	ClaimEmailSends(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]EmailSend, error)
	// FinishEmailSend records an attempt's outcome. A failure (retry or
	// dead) is recorded only while the send is still sending with attempts
	// equal to attempt, so a sender whose lease expired cannot overwrite the
	// sender that took over. Sent is recorded from any attempt unless the
	// send is already sent: the email went out, and forgetting that is what
	// would send it again.
	FinishEmailSend(ctx context.Context, sendID string, attempt int, outcome EmailOutcome) error
}

// Action is a permission check, numbered as the proto's Action enum.
type Action uint8

// The actions this service checks.
const (
	ActionNoteRead    Action = 1
	ActionNoteComment Action = 2
	ActionNoteEdit    Action = 3
	// ActionNoteCreate on a workspace needs member or above, which is how
	// the access rule tells a member from a guest.
	ActionNoteCreate Action = 4
)

// Check is one (action, resource) question.
type Check struct {
	Action     Action
	ResourceID string
}

// Authorizer answers permission questions for a user: identity's Authorize.
type Authorizer interface {
	// Authorize returns one answer per check, in order. at is when the fact
	// being notified about happened. The identity adapter answers for the
	// present (events are processed seconds after they happen); the seed's
	// fake answers at at, so replayed history is judged as it was.
	Authorize(ctx context.Context, userID string, checks []Check, at time.Time) ([]bool, error)
}

// Profile is what notifications need to know about a user.
type Profile struct {
	UserID      string
	DisplayName string
	Email       string
}

// Directory looks users up in identity.
type Directory interface {
	// Profile returns ErrUnknownUser for a user identity does not know.
	Profile(ctx context.Context, userID string) (Profile, error)
	// ResolveHandles maps @handles to the IDs of workspace members with
	// those handles; unknown handles are absent from the result.
	ResolveHandles(ctx context.Context, workspaceID string, handles []string) (map[string]string, error)
}

// Mailer sends one email. A failure wrapping ErrPermanent is not retried.
type Mailer interface {
	Send(ctx context.Context, msg email.Message) error
}

// Fanout carries live notifications to every replica that has a
// subscriber for the recipient (Redis pub/sub keyed by user). Delivery is
// best effort: a client that misses one sees it in ListInbox.
type Fanout interface {
	Publish(ctx context.Context, n domain.Notification) error
	// Join starts delivering the user's notifications to this replica's hub;
	// Leave stops it. The hub calls them on its first and last local
	// subscriber for the user.
	Join(ctx context.Context, userID string) error
	Leave(ctx context.Context, userID string) error
}

// UnreadCache caches each user's unread count.
type UnreadCache interface {
	// Get returns the cached count or loads, caches and returns it.
	Get(ctx context.Context, userID string, load func(context.Context) (uint32, error)) (uint32, error)
	// Invalidate drops the users' counts; called after every commit that
	// changes them.
	Invalidate(ctx context.Context, userIDs ...string) error
}

// Clock returns the current time; tests and the seed loader fix it.
type Clock func() time.Time

// IDGenerator returns a new UUIDv7 string.
type IDGenerator func() string
