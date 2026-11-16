package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
)

// IndexConfig tunes the follower and the snapshot policy.
type IndexConfig struct {
	// PollInterval is how often Run checks the change feed when nothing
	// notifies it (another replica's consumer applied the event). Default 1s.
	PollInterval time.Duration
	// BatchSize is the most changes applied under one write lock. Default 500.
	BatchSize int
	// SnapshotEvery takes a snapshot once this many changes have been
	// applied since the last one (default 1000), and SnapshotInterval at
	// least this often when anything changed (default 10 minutes).
	SnapshotEvery    int
	SnapshotInterval time.Duration
	Logger           *slog.Logger
	// OnApplied, when set, is called after each batch with the notes it
	// changed (the service invalidates their cache tags).
	OnApplied func(ctx context.Context, notes []string)
}

func (c *IndexConfig) defaults() {
	if c.PollInterval <= 0 {
		c.PollInterval = time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 500
	}
	if c.SnapshotEvery <= 0 {
		c.SnapshotEvery = 1000
	}
	if c.SnapshotInterval <= 0 {
		c.SnapshotInterval = 10 * time.Minute
	}
	if c.Logger == nil {
		c.Logger = logging.Discard()
	}
}

// Index holds this replica's in-memory projection and keeps it in step
// with the durable one by following the change feed.
//
// Why a feed and not the events themselves: Pub/Sub delivers each event to
// one replica of the subscription, but every replica needs every change in
// memory. The consuming replica writes note states and a feed entry in one
// transaction (Store.Apply); every replica, itself included, then reads the
// feed and loads the changed notes' current states. Feed positions are
// assigned under a lock in commit order, so reading past a position never
// skips a change committed later.
//
// Restart: the newest snapshot (the encoded projection and the position it
// reflects) is decoded and the feed is replayed from that position, so
// only the changes since the snapshot are re-read. Without a snapshot, or
// when the feed was pruned past the position, every note is loaded from
// the state tables. Rebuilding from events is the last resort, described in
// the service README.
type Index struct {
	cfg   IndexConfig
	store Store

	mu   sync.RWMutex
	proj *domain.Projection

	notify chan struct{}
	// Snapshot bookkeeping is touched by Run, by consumers asking for a
	// snapshot after a purge, and by tests, so it is atomic.
	sinceSnapshot   atomic.Int64
	lastSnapshot    atomic.Int64 // unix nanoseconds
	snapshotPending atomic.Bool
}

// NewIndex returns an empty index over store. Call Bootstrap before
// serving.
func NewIndex(store Store, cfg IndexConfig) *Index {
	cfg.defaults()
	ix := &Index{cfg: cfg, store: store, proj: domain.NewProjection(), notify: make(chan struct{}, 1)}
	ix.lastSnapshot.Store(time.Now().UnixNano())
	return ix
}

// Read runs fn with the projection under the read lock. fn must not keep
// references to it.
func (ix *Index) Read(fn func(p *domain.Projection) error) error {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return fn(ix.proj)
}

// Position returns the change-feed position the projection reflects.
func (ix *Index) Position() int64 {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.proj.Position
}

// Notify asks Run to catch up now rather than at the next poll.
func (ix *Index) Notify() {
	select {
	case ix.notify <- struct{}{}:
	default:
	}
}

// Bootstrap restores the projection from the newest snapshot, or from the
// state tables, then catches up with the feed.
func (ix *Index) Bootstrap(ctx context.Context) error {
	start := time.Now()
	snap, found, err := ix.store.LatestSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}
	from := "tables"
	if found {
		proj, err := domain.DecodeSnapshot(snap.Data)
		if err != nil {
			// A bad snapshot costs a full load, never an outage.
			ix.cfg.Logger.WarnContext(ctx, "snapshot unusable, loading every note", "error", err, "position", snap.Position)
			found = false
		} else {
			ix.setProjection(proj)
			from = "snapshot"
		}
	}
	if !found {
		if err := ix.reload(ctx); err != nil {
			return err
		}
	}
	applied, err := ix.CatchUp(ctx)
	if err != nil {
		return err
	}
	ix.cfg.Logger.InfoContext(ctx, "projection ready", "from", from, "position", ix.Position(),
		"replayed_changes", applied, "duration_s", time.Since(start).Seconds())
	return nil
}

func (ix *Index) setProjection(p *domain.Projection) {
	ix.mu.Lock()
	ix.proj = p
	ix.mu.Unlock()
}

// reload builds the projection from every stored note.
func (ix *Index) reload(ctx context.Context) error {
	var states []*domain.NoteState
	position, err := ix.store.LoadAll(ctx, func(n *domain.NoteState) error {
		states = append(states, n)
		return nil
	})
	if err != nil {
		return fmt.Errorf("load notes: %w", err)
	}
	// Versions are stamped with the load position: cache keys include them,
	// so two replicas that loaded at different positions must never share a
	// version for different contents.
	proj := domain.NewProjection()
	for _, n := range states {
		proj.Apply(n.ID, n, position)
	}
	proj.Position = position
	ix.setProjection(proj)
	ix.snapshotPending.Store(true)
	return nil
}

// CatchUp applies every change after the projection's position and
// returns how many it applied.
func (ix *Index) CatchUp(ctx context.Context) (int, error) {
	total := 0
	for {
		changes, err := ix.store.Changes(ctx, ix.Position(), ix.cfg.BatchSize)
		if errors.Is(err, ErrFeedGap) {
			ix.cfg.Logger.WarnContext(ctx, "change feed pruned past our position, reloading", "position", ix.Position())
			if err := ix.reload(ctx); err != nil {
				return total, err
			}
			continue
		}
		if err != nil {
			return total, fmt.Errorf("read change feed: %w", err)
		}
		if len(changes) == 0 {
			return total, nil
		}
		notes, err := ix.applyBatch(ctx, changes)
		if err != nil {
			return total, err
		}
		total += len(changes)
		ix.sinceSnapshot.Add(int64(len(changes)))
		if ix.cfg.OnApplied != nil {
			ix.cfg.OnApplied(ctx, notes)
		}
	}
}

// applyBatch loads the current state of each note in the batch and applies
// it once, stamped with the note's last position in the batch.
func (ix *Index) applyBatch(ctx context.Context, changes []Change) ([]string, error) {
	last := map[string]int64{}
	for _, c := range changes {
		last[c.NoteID] = c.Seq
	}
	ids := make([]string, 0, len(last))
	for id := range last {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return cmp.Compare(last[a], last[b]) })
	states, err := ix.store.LoadNotes(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load changed notes: %w", err)
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, id := range ids {
		ix.proj.Apply(id, states[id], last[id])
	}
	ix.proj.Position = changes[len(changes)-1].Seq
	return ids, nil
}

// Snapshot encodes the projection and stores it.
func (ix *Index) Snapshot(ctx context.Context) error {
	ix.mu.RLock()
	data, err := domain.EncodeSnapshot(ix.proj)
	position := ix.proj.Position
	ix.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	if err := ix.store.SaveSnapshot(ctx, Snapshot{Position: position, Data: data}); err != nil {
		return fmt.Errorf("save snapshot: %w", err)
	}
	ix.sinceSnapshot.Store(0)
	ix.lastSnapshot.Store(time.Now().UnixNano())
	ix.snapshotPending.Store(false)
	ix.cfg.Logger.InfoContext(ctx, "snapshot saved", "position", position, "bytes", len(data))
	return nil
}

func (ix *Index) snapshotDue() bool {
	since := ix.sinceSnapshot.Load()
	age := time.Since(time.Unix(0, ix.lastSnapshot.Load()))
	return ix.snapshotPending.Load() || since >= int64(ix.cfg.SnapshotEvery) || since > 0 && age >= ix.cfg.SnapshotInterval
}

// Run follows the feed until ctx ends, taking snapshots by the policy in
// IndexConfig. Failures are logged and retried at the next tick; the
// projection only ever moves forward.
func (ix *Index) Run(ctx context.Context) error {
	ticker := time.NewTicker(ix.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-ix.notify:
		}
		if _, err := ix.CatchUp(ctx); err != nil && ctx.Err() == nil {
			ix.cfg.Logger.ErrorContext(ctx, "catch up failed", "error", err)
			continue
		}
		if ix.snapshotDue() {
			if err := ix.Snapshot(ctx); err != nil && ctx.Err() == nil {
				ix.cfg.Logger.ErrorContext(ctx, "snapshot failed", "error", err)
			}
		}
	}
}

// RequestSnapshot makes the next Run tick write a snapshot (after a purge
// deleted the old ones).
func (ix *Index) RequestSnapshot() {
	ix.snapshotPending.Store(true)
	ix.Notify()
}
