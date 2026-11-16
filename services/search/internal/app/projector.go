package app

import (
	"context"
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
)

// Consumer names: the Pub/Sub subscriptions in
// deploy/compose/pubsub-init.sh, also the keys of processed events and
// projection positions.
const (
	DocumentsConsumer = "search.documents-events"
	IdentityConsumer  = "search.identity-events"
)

// Outcome is what handling one event did, for the consumer metrics.
type Outcome string

const (
	OutcomeApplied   Outcome = "applied"
	OutcomeDuplicate Outcome = "duplicate"
	// OutcomeIgnored: the event type does not concern search; it is still
	// recorded as processed.
	OutcomeIgnored Outcome = "ignored"
)

// TagInvalidator drops cached entries by tag (redisx.Invalidator).
type TagInvalidator interface {
	InvalidateTags(ctx context.Context, tags ...string) (int, error)
}

// Projector applies documents.events and identity.events to the durable
// projection, idempotently: each event is recorded as processed in the same
// transaction as its effect (Store.Apply), so a redelivery is a no-op.
type Projector struct {
	store  Store
	index  *Index // notified after an applied change; may be nil
	tags   TagInvalidator
	logger *slog.Logger
}

// NewProjector returns a projector. index and tags may be nil.
func NewProjector(store Store, index *Index, tags TagInvalidator, logger *slog.Logger) *Projector {
	if logger == nil {
		logger = logging.Discard()
	}
	return &Projector{store: store, index: index, tags: tags, logger: logger}
}

// DecodeEvent parses an event payload. A payload that does not decode is
// ErrUnprocessable: no retry will fix it.
func DecodeEvent(payload []byte) (*skeinv1.Event, error) {
	var e skeinv1.Event
	if err := proto.Unmarshal(payload, &e); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnprocessable, err)
	}
	return &e, nil
}

// MutationOf turns a documents event into a mutation; nil when search has
// nothing to do with the event.
func MutationOf(e *skeinv1.Event) *domain.Mutation {
	switch p := e.GetPayload().(type) {
	case *skeinv1.Event_NoteCreated:
		return &domain.Mutation{Kind: domain.MutationCreate, NoteID: p.NoteCreated.GetNoteId(),
			WorkspaceID: p.NoteCreated.GetWorkspaceId(), Title: p.NoteCreated.GetTitle()}
	case *skeinv1.Event_NoteRenamed:
		return &domain.Mutation{Kind: domain.MutationRename, NoteID: p.NoteRenamed.GetNoteId(), Title: p.NoteRenamed.GetTitle()}
	case *skeinv1.Event_NoteTrashed:
		return &domain.Mutation{Kind: domain.MutationTrash, NoteID: p.NoteTrashed.GetNoteId()}
	case *skeinv1.Event_NoteRestored:
		return &domain.Mutation{Kind: domain.MutationRestore, NoteID: p.NoteRestored.GetNoteId()}
	case *skeinv1.Event_NotePurged:
		return &domain.Mutation{Kind: domain.MutationPurge, NoteID: p.NotePurged.GetNoteId()}
	case *skeinv1.Event_BlocksChanged:
		m := &domain.Mutation{Kind: domain.MutationBlocks, NoteID: p.BlocksChanged.GetNoteId(),
			RemovedBlocks: p.BlocksChanged.GetRemovedBlockIds()}
		for _, b := range p.BlocksChanged.GetBlocks() {
			m.Blocks = append(m.Blocks, domain.Block{ID: b.GetBlockId(), Kind: b.GetKind(), Language: b.GetLanguage(), Text: b.GetText()})
		}
		return m
	case *skeinv1.Event_LinksChanged:
		return &domain.Mutation{Kind: domain.MutationLinks, NoteID: p.LinksChanged.GetNoteId(), Links: p.LinksChanged.GetTargets()}
	}
	return nil
}

// EventType names an event's payload for logs and metrics.
func EventType(e *skeinv1.Event) string {
	if m := MutationOf(e); m != nil {
		return m.Kind.String()
	}
	if e.GetAccessChanged() != nil {
		return "access_changed"
	}
	return "other"
}

// HandleDocuments applies one documents.events message.
func (p *Projector) HandleDocuments(ctx context.Context, msg events.Message) (Outcome, error) {
	e, err := DecodeEvent(msg.Payload)
	if err != nil {
		return "", err
	}
	m := MutationOf(e)
	if m != nil {
		if err := m.Validate(); err != nil {
			return "", fmt.Errorf("%w: %v", ErrUnprocessable, err)
		}
	}
	first, err := p.store.Apply(ctx, DocumentsConsumer, msg.ID, m)
	if err != nil {
		return "", err
	}
	switch {
	case !first:
		return OutcomeDuplicate, nil
	case m == nil:
		return OutcomeIgnored, nil
	}
	// Cached results that show this note are stale (or, after a purge, must
	// not survive at all). Every replica's follower invalidates again once it
	// has applied the change, closing the window in between.
	p.invalidate(ctx, "note:"+m.NoteID)
	if p.index != nil {
		if m.Kind == domain.MutationPurge {
			p.index.RequestSnapshot()
		}
		p.index.Notify()
	}
	return OutcomeApplied, nil
}

// HandleIdentity applies one identity.events message: an access change
// drops the cached readable-note lists and search results it affects. A
// change naming a user affects that user; one naming only a workspace or a
// note (a share link, a workspace setting) affects everyone in the
// workspace.
func (p *Projector) HandleIdentity(ctx context.Context, msg events.Message) (Outcome, error) {
	e, err := DecodeEvent(msg.Payload)
	if err != nil {
		return "", err
	}
	ac := e.GetAccessChanged()
	if ac != nil {
		tags := []string{WorkspaceTag(ac.GetWorkspaceId())}
		if ac.GetUserId() != "" {
			tags = []string{UserTag(ac.GetUserId())}
		}
		// Invalidate before recording the event as processed: invalidation is
		// idempotent, so a failure here is retried by redelivery, whereas a
		// failure after the record would leave a duplicate that skips it.
		if err := p.invalidateStrict(ctx, tags...); err != nil {
			return "", err
		}
	}
	first, err := p.store.Apply(ctx, IdentityConsumer, msg.ID, nil)
	switch {
	case err != nil:
		return "", err
	case !first:
		return OutcomeDuplicate, nil
	case ac == nil:
		return OutcomeIgnored, nil
	}
	return OutcomeApplied, nil
}

func (p *Projector) invalidate(ctx context.Context, tags ...string) {
	if err := p.invalidateStrict(ctx, tags...); err != nil {
		p.logger.WarnContext(ctx, "cache invalidation failed; entries expire by TTL", "error", err)
	}
}

func (p *Projector) invalidateStrict(ctx context.Context, tags ...string) error {
	if p.tags == nil {
		return nil
	}
	if _, err := p.tags.InvalidateTags(ctx, tags...); err != nil {
		return fmt.Errorf("%w: invalidate %v: %v", ErrUnavailable, tags, err)
	}
	return nil
}

// UserTag and WorkspaceTag tag cache entries derived from a user's or a
// workspace's access rights.
func UserTag(user string) string    { return "acl:user:" + user }
func WorkspaceTag(ws string) string { return "acl:ws:" + ws }
func noteTag(note string) string    { return "note:" + note }
