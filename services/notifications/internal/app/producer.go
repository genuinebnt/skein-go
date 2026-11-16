package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

// Event is the envelope metadata every producer input carries.
type Event struct {
	// Consumer is the subscription name; with ID it is the idempotency key.
	Consumer   string
	ID         string
	OccurredAt time.Time
	// ActorUserID is who caused the event (Event.actor.user_id); for an
	// agent run, the user it acts for.
	ActorUserID     string
	ActorAgentRunID string
}

// Outcome reports what an event did.
type Outcome string

// Outcomes of Producer methods.
const (
	OutcomeApplied   Outcome = "applied"
	OutcomeDuplicate Outcome = "duplicate"
)

// ProducerConfig wires a Producer. Zero durations take the defaults.
type ProducerConfig struct {
	Store       Store
	Authorizer  Authorizer
	Directory   Directory
	UnreadCache UnreadCache
	Fanout      Fanout
	Clock       Clock
	NewID       IDGenerator
	// CoalesceWindow is how far back an unread notification with the same
	// coalescing key absorbs a new one; 10 minutes.
	CoalesceWindow time.Duration
	// DigestDelay holds each email job this long before it is due, so a
	// burst becomes one digest; 1 minute.
	DigestDelay time.Duration
	// EmailEnqueued is called after a commit that enqueued email, to wake
	// the sender. May be nil.
	EmailEnqueued func()
	Logger        *slog.Logger
	MeterProvider metric.MeterProvider
}

// Producer turns events into notifications. See the package documentation.
type Producer struct {
	cfg     ProducerConfig
	metrics producerMetrics
}

type producerMetrics struct {
	placed     metric.Int64Counter
	suppressed metric.Int64Counter
	afterwards metric.Int64Counter
}

// NewProducer checks the wiring and returns a Producer.
func NewProducer(cfg ProducerConfig) (*Producer, error) {
	if cfg.Store == nil || cfg.Authorizer == nil || cfg.Directory == nil || cfg.UnreadCache == nil || cfg.Fanout == nil {
		return nil, errors.New("app: producer needs a store, authorizer, directory, unread cache and fanout")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.NewID == nil {
		return nil, errors.New("app: producer needs an ID generator")
	}
	if cfg.CoalesceWindow == 0 {
		cfg.CoalesceWindow = 10 * time.Minute
	}
	if cfg.DigestDelay == 0 {
		cfg.DigestDelay = time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.MeterProvider == nil {
		cfg.MeterProvider = otel.GetMeterProvider()
	}
	meter := cfg.MeterProvider.Meter("github.com/genuinebnt/skein-go/services/notifications/internal/app")
	var m producerMetrics
	var errs [3]error
	m.placed, errs[0] = meter.Int64Counter("skein.notifications.placed", metric.WithUnit("{notification}"),
		metric.WithDescription("Candidates placed, by kind and outcome (inserted, refreshed, coalesced, stale)."))
	m.suppressed, errs[1] = meter.Int64Counter("skein.notifications.suppressed", metric.WithUnit("{notification}"),
		metric.WithDescription("Candidates dropped, by kind and reason (unreadable, preferences)."))
	m.afterwards, errs[2] = meter.Int64Counter("skein.notifications.post_commit.errors", metric.WithUnit("{error}"),
		metric.WithDescription("Best-effort steps after a commit that failed, by step (cache, fanout)."))
	if err := errors.Join(errs[:]...); err != nil {
		return nil, fmt.Errorf("app: metrics: %w", err)
	}
	return &Producer{cfg: cfg, metrics: m}, nil
}

// readPair is a (user, note) read check to make before the transaction.
type readPair struct{ user, note string }

// readSet holds the answers. A pair that was never asked is unreadable, so
// a rule that forgot to ask fails closed.
type readSet map[readPair]bool

func (r readSet) readable(user, note string) bool { return r[readPair{user, note}] }

// placement collects what a transaction did, for the steps after commit.
type placement struct {
	duplicate     bool
	inboxChanged  []string
	live          []domain.Notification
	emailEnqueued bool
}

func (pl *placement) changed(users ...string) { pl.inboxChanged = append(pl.inboxChanged, users...) }

// commitFunc runs inside the event's transaction and returns the
// candidates to place.
type commitFunc func(ctx context.Context, tx Tx, pl *placement) ([]domain.Candidate, error)

// apply is the shared path of every event; see the package documentation.
func (p *Producer) apply(ctx context.Context, ev Event, pairs []readPair, commit commitFunc) (Outcome, error) {
	if ev.Consumer == "" || !domain.ValidUUID(ev.ID) {
		// Event IDs are UUIDv7 (events.proto); the processed-events table
		// stores them as uuid.
		return "", fmt.Errorf("%w: event needs a consumer and a UUID id", domain.ErrInvalidArgument)
	}
	reads, err := p.checkReads(ctx, pairs, ev.OccurredAt)
	if err != nil {
		return "", err
	}
	var pl placement
	err = p.cfg.Store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		pl = placement{}
		first, err := tx.MarkProcessed(ctx, ev.Consumer, ev.ID)
		if err != nil {
			return err
		}
		if !first {
			pl.duplicate = true
			return nil
		}
		candidates, err := commit(ctx, tx, &pl)
		if err != nil {
			return err
		}
		prefs := map[string]domain.Preferences{}
		for _, c := range candidates {
			if c.NeedsRead != "" && !reads.readable(c.RecipientID, c.NeedsRead) {
				p.metrics.suppressed.Add(ctx, 1, metric.WithAttributes(kindAttr(c.Kind), attribute.String("reason", "unreadable")))
				continue
			}
			if err := p.place(ctx, tx, c, prefs, &pl); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if pl.duplicate {
		return OutcomeDuplicate, nil
	}
	p.afterCommit(ctx, pl)
	return OutcomeApplied, nil
}

func kindAttr(k domain.Kind) attribute.KeyValue {
	return attribute.String("skein.notification.kind", k.String())
}

// checkReads asks the authorizer every read question, one call per user.
func (p *Producer) checkReads(ctx context.Context, pairs []readPair, at time.Time) (readSet, error) {
	notesByUser := map[string][]string{}
	var users []string
	for _, pr := range pairs {
		if pr.user == "" || pr.note == "" {
			continue
		}
		if _, ok := notesByUser[pr.user]; !ok {
			users = append(users, pr.user)
		}
		if !slices.Contains(notesByUser[pr.user], pr.note) {
			notesByUser[pr.user] = append(notesByUser[pr.user], pr.note)
		}
	}
	slices.Sort(users)
	reads := readSet{}
	for _, u := range users {
		notes := notesByUser[u]
		checks := make([]Check, len(notes))
		for i, n := range notes {
			checks[i] = Check{Action: ActionNoteRead, ResourceID: n}
		}
		allowed, err := p.cfg.Authorizer.Authorize(ctx, u, checks, at)
		if err != nil {
			return nil, fmt.Errorf("app: authorize: %w", err)
		}
		if len(allowed) != len(checks) {
			return nil, fmt.Errorf("app: authorize returned %d answers for %d checks", len(allowed), len(checks))
		}
		for i, n := range notes {
			reads[readPair{u, n}] = allowed[i]
		}
	}
	return reads, nil
}

// place stores one candidate according to the recipient's preferences:
// refresh the notification from the same source, absorb into a recent one
// with the same coalescing key, or insert.
func (p *Producer) place(ctx context.Context, tx Tx, c domain.Candidate, prefs map[string]domain.Preferences, pl *placement) error {
	pref, err := p.preference(ctx, tx, prefs, c.RecipientID, c.Kind)
	if err != nil {
		return err
	}
	if !pref.InApp && !pref.Email {
		p.metrics.suppressed.Add(ctx, 1, metric.WithAttributes(kindAttr(c.Kind), attribute.String("reason", "preferences")))
		return nil
	}
	outcome := func(o string) {
		p.metrics.placed.Add(ctx, 1, metric.WithAttributes(kindAttr(c.Kind), attribute.String("outcome", o)))
	}

	existing, found, err := tx.NotificationBySource(ctx, c.RecipientID, c.Source)
	if err != nil {
		return err
	}
	if found {
		if !c.At.After(existing.UpdatedAt) {
			outcome("stale")
			return nil
		}
		n := c.Build(existing.ID, pref.InApp)
		n.Occurrences = existing.Occurrences + 1
		if err := n.Validate(); err != nil {
			return err
		}
		if err := tx.UpdateNotification(ctx, n); err != nil {
			return err
		}
		outcome("refreshed")
		pl.changed(n.RecipientID, existing.RecipientID)
		return p.announce(ctx, tx, n, pref, pl)
	}

	if c.CoalesceKey != "" {
		prev, found, err := tx.LatestCoalescible(ctx, c.RecipientID, c.CoalesceKey, c.At.Add(-p.cfg.CoalesceWindow))
		if err != nil {
			return err
		}
		if found && prev.InInbox == pref.InApp {
			prev.Occurrences++
			prev.Body = domain.Truncate(c.Body, domain.MaxBodyLength)
			if at := domain.Microseconds(c.At); at.After(prev.UpdatedAt) {
				prev.UpdatedAt = at
			}
			if err := prev.Validate(); err != nil {
				return err
			}
			outcome("coalesced")
			return tx.UpdateNotification(ctx, prev)
		}
	}

	n := c.Build(p.cfg.NewID(), pref.InApp)
	if err := n.Validate(); err != nil {
		return err
	}
	if err := tx.InsertNotification(ctx, n); err != nil {
		return err
	}
	outcome("inserted")
	if n.InInbox {
		pl.changed(n.RecipientID)
	}
	return p.announce(ctx, tx, n, pref, pl)
}

// announce queues the live push and the email for a new or refreshed
// notification.
func (p *Producer) announce(ctx context.Context, tx Tx, n domain.Notification, pref domain.Preference, pl *placement) error {
	if n.InInbox {
		pl.live = append(pl.live, n)
	}
	if !pref.Email {
		return nil
	}
	now := p.cfg.Clock()
	job := EmailJob{ID: p.cfg.NewID(), RecipientID: n.RecipientID, NotificationID: n.ID,
		CreatedAt: domain.Microseconds(now), DueAt: domain.Microseconds(now.Add(p.cfg.DigestDelay))}
	if err := tx.EnqueueEmail(ctx, job); err != nil {
		return err
	}
	pl.emailEnqueued = true
	return nil
}

func (p *Producer) preference(ctx context.Context, tx Tx, cache map[string]domain.Preferences, user string, k domain.Kind) (domain.Preference, error) {
	prefs, ok := cache[user]
	if !ok {
		stored, err := tx.Preferences(ctx, user)
		if err != nil {
			return domain.Preference{}, err
		}
		prefs = domain.PreferencesFrom(stored)
		cache[user] = prefs
	}
	return prefs.For(k), nil
}

// afterCommit runs the best-effort steps: cache invalidation, live push,
// waking the email sender.
func (p *Producer) afterCommit(ctx context.Context, pl placement) {
	if len(pl.inboxChanged) > 0 {
		users := slices.Compact(slices.Sorted(slices.Values(pl.inboxChanged)))
		if err := p.cfg.UnreadCache.Invalidate(ctx, users...); err != nil {
			p.metrics.afterwards.Add(ctx, 1, metric.WithAttributes(attribute.String("step", "cache")))
			p.cfg.Logger.WarnContext(ctx, "unread count invalidation failed", slog.Int("users", len(users)), slog.String("error", err.Error()))
		}
	}
	for _, n := range pl.live {
		if err := p.cfg.Fanout.Publish(ctx, n); err != nil {
			p.metrics.afterwards.Add(ctx, 1, metric.WithAttributes(attribute.String("step", "fanout")))
			p.cfg.Logger.WarnContext(ctx, "live push failed", slog.String("notification_id", n.ID), slog.String("error", err.Error()))
		}
	}
	if pl.emailEnqueued && p.cfg.EmailEnqueued != nil {
		p.cfg.EmailEnqueued()
	}
}

// displayName returns the user's name for titles, or a neutral word when
// the directory cannot say. A missing name never blocks a notification.
func (p *Producer) displayName(ctx context.Context, userID string) string {
	if userID == "" {
		return "Someone"
	}
	prof, err := p.cfg.Directory.Profile(ctx, userID)
	if err != nil || prof.DisplayName == "" {
		if err != nil && !errors.Is(err, ErrUnknownUser) {
			p.cfg.Logger.WarnContext(ctx, "display name lookup failed", slog.String("user_id", userID), slog.String("error", err.Error()))
		}
		return "Someone"
	}
	return prof.DisplayName
}

// note reads the note projection in its own short transaction, before the
// event's transaction, because the rules need it to decide which network
// lookups to make.
func (p *Producer) note(ctx context.Context, noteID string) (NoteInfo, bool, error) {
	var info NoteInfo
	var found bool
	err := p.cfg.Store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		info, found, err = tx.Note(ctx, noteID)
		return err
	})
	return info, found, err
}

func pairsOf(candidates []domain.Candidate) []readPair {
	var out []readPair
	for _, c := range candidates {
		if c.NeedsRead != "" {
			out = append(out, readPair{c.RecipientID, c.NeedsRead})
		}
	}
	return out
}

func fixed(candidates []domain.Candidate) commitFunc {
	return func(context.Context, Tx, *placement) ([]domain.Candidate, error) { return candidates, nil }
}
