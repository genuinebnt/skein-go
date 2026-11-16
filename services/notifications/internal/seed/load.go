package seed

import (
	"context"
	"fmt"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

// loadBatch is how many notifications one transaction stores.
const loadBatch = 200

// LoadStats counts what LoadInbox did.
type LoadStats struct {
	Loaded, AlreadyPresent int
}

// ToDomain converts an expected-inbox line to a notification, keeping its
// ID. A read notification is read from the moment it was created: the
// dataset does not say when it was read.
func (n Notification) ToDomain() (domain.Notification, error) {
	kind, err := domain.ParseKind(n.Kind)
	if err != nil {
		return domain.Notification{}, err
	}
	at := domain.Microseconds(n.CreatedAt)
	out := domain.Notification{
		ID: n.ID, RecipientID: n.UserID, Kind: kind, Title: n.Title, Body: n.Body, ActorID: n.ActorID,
		Source: domain.Source{Kind: domain.SourceKind(n.Source.Kind), ID: n.Source.ID}, Occurrences: 1, InInbox: true,
		CreatedAt: at, UpdatedAt: at,
	}
	if n.Location != nil {
		out.Location = domain.Location{NoteID: n.Location.NoteID, BlockID: n.Location.BlockID, Start: n.Location.Start, End: n.Location.End}
	}
	if n.Read {
		out.ReadAt = at
	}
	return out, out.Validate()
}

// LoadInbox stores the dataset's expected inbox through the repository,
// keyed by the dataset's IDs and sources, so loading twice changes
// nothing. It enqueues no email.
func LoadInbox(ctx context.Context, store app.Store, d *Dataset) (LoadStats, error) {
	var stats LoadStats
	for start := 0; start < len(d.Notifications); start += loadBatch {
		batch := d.Notifications[start:min(start+loadBatch, len(d.Notifications))]
		var loaded, present int
		err := store.WithTx(ctx, func(ctx context.Context, tx app.Tx) error {
			loaded, present = 0, 0
			for _, line := range batch {
				n, err := line.ToDomain()
				if err != nil {
					return fmt.Errorf("seed: notification %s: %w", line.ID, err)
				}
				if _, found, err := tx.NotificationBySource(ctx, n.RecipientID, n.Source); err != nil {
					return err
				} else if found {
					present++
					continue
				}
				if err := tx.InsertNotification(ctx, n); err != nil {
					return fmt.Errorf("seed: notification %s: %w", line.ID, err)
				}
				loaded++
			}
			return nil
		})
		if err != nil {
			return stats, err
		}
		stats.Loaded += loaded
		stats.AlreadyPresent += present
	}
	return stats, nil
}
