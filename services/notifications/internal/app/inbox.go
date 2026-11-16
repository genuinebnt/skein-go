package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

// MaxMarkRead bounds the IDs one MarkRead call may list.
const MaxMarkRead = 500

// Inbox serves the caller-facing RPCs: ListInbox, MarkRead and the
// preference pair. Every method acts on the given user's own data only.
type Inbox struct {
	store  Store
	cache  UnreadCache
	clock  Clock
	logger *slog.Logger
}

// NewInbox returns the inbox use cases.
func NewInbox(store Store, cache UnreadCache, clock Clock, logger *slog.Logger) *Inbox {
	if clock == nil {
		clock = time.Now
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Inbox{store: store, cache: cache, clock: clock, logger: logger}
}

// InboxPage is one page of ListInbox.
type InboxPage struct {
	Notifications []domain.Notification
	// NextPageToken is empty on the last page.
	NextPageToken string
	// Unread is the whole inbox's unread count, whatever the page.
	Unread uint32
}

// ListInbox returns a page of the user's inbox, newest first.
func (in *Inbox) ListInbox(ctx context.Context, userID string, pageSize uint32, pageToken string, unreadOnly bool) (InboxPage, error) {
	q := InboxQuery{RecipientID: userID, UnreadOnly: unreadOnly, Limit: domain.PageSize(pageSize) + 1}
	if pageToken != "" {
		cursor, err := domain.DecodeInboxCursor(pageToken, unreadOnly)
		if err != nil {
			return InboxPage{}, err
		}
		q.After = &cursor
	}
	var page InboxPage
	err := in.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		items, err := tx.ListInbox(ctx, q)
		page.Notifications = items
		return err
	})
	if err != nil {
		return InboxPage{}, err
	}
	if len(page.Notifications) == q.Limit {
		page.Notifications = page.Notifications[:q.Limit-1]
		last := page.Notifications[len(page.Notifications)-1]
		page.NextPageToken = domain.InboxCursor{CreatedAt: last.CreatedAt, ID: last.ID, UnreadOnly: unreadOnly}.Encode()
	}
	page.Unread, err = in.UnreadCount(ctx, userID)
	return page, err
}

// UnreadCount returns the user's unread count, from the cache when it has
// it.
func (in *Inbox) UnreadCount(ctx context.Context, userID string) (uint32, error) {
	return in.cache.Get(ctx, userID, func(ctx context.Context) (uint32, error) {
		var n uint32
		err := in.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
			var err error
			n, err = tx.CountUnread(ctx, userID)
			return err
		})
		return n, err
	})
}

// MarkRead marks the user's notifications among ids as read. Unknown,
// malformed, already read and other users' IDs are ignored, so the call is
// idempotent and reveals nothing about IDs that are not the caller's.
func (in *Inbox) MarkRead(ctx context.Context, userID string, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("%w: ids must not be empty", domain.ErrInvalidArgument)
	}
	if len(ids) > MaxMarkRead {
		return fmt.Errorf("%w: at most %d ids per call", domain.ErrInvalidArgument, MaxMarkRead)
	}
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if b, ok := domain.ParseUUID(id); ok {
			valid = append(valid, domain.FormatUUID(b))
		}
	}
	if len(valid) == 0 {
		return nil
	}
	var changed int
	err := in.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		changed, err = tx.MarkRead(ctx, userID, valid, domain.Microseconds(in.clock()))
		return err
	})
	if err != nil || changed == 0 {
		return err
	}
	if err := in.cache.Invalidate(ctx, userID); err != nil {
		// The commit stands; the count is stale until the entry's TTL.
		in.logger.WarnContext(ctx, "unread count invalidation failed", slog.String("error", err.Error()))
	}
	return nil
}

// Preferences returns the user's settings for every kind.
func (in *Inbox) Preferences(ctx context.Context, userID string) (domain.Preferences, error) {
	var prefs domain.Preferences
	err := in.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		stored, err := tx.Preferences(ctx, userID)
		prefs = domain.PreferencesFrom(stored)
		return err
	})
	return prefs, err
}

// UpdatePreferences sets the listed kinds; the others keep their setting.
func (in *Inbox) UpdatePreferences(ctx context.Context, userID string, updates []domain.Preference) error {
	if err := domain.ValidateUpdate(updates); err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	return in.store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		return tx.SavePreferences(ctx, userID, updates, domain.Microseconds(in.clock()))
	})
}

// IsInvalidArgument reports whether err is the caller's fault.
func IsInvalidArgument(err error) bool { return errors.Is(err, domain.ErrInvalidArgument) }
