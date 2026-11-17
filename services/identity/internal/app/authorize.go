package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// loadFacts reads every row the checks can touch for subject: the notes,
// their workspaces and the workspaces named directly, the subject's
// memberships of those workspaces and overrides on those notes, and the
// share link when the subject is one. Malformed resource IDs are skipped;
// Decide then denies them as unknown.
func loadFacts(ctx context.Context, tx Tx, subj domain.Subject, checks []domain.Check) (*domain.Facts, error) {
	f := domain.NewFacts()
	var noteIDs []domain.NoteID
	var wsIDs []domain.WorkspaceID
	for _, c := range checks {
		id, err := domain.ParseUUID("resource_id", c.ResourceID)
		if err != nil {
			continue
		}
		switch c.Action.Resource() {
		case domain.ResourceNote:
			noteIDs = append(noteIDs, domain.NoteID(id))
		case domain.ResourceWorkspace:
			wsIDs = append(wsIDs, domain.WorkspaceID(id))
		}
	}
	slices.Sort(noteIDs)
	noteIDs = slices.Compact(noteIDs)
	notes, err := tx.Notes(ctx, noteIDs)
	if err != nil {
		return nil, err
	}
	for _, n := range notes {
		f.AddNote(n)
		wsIDs = append(wsIDs, n.WorkspaceID)
	}
	slices.Sort(wsIDs)
	wsIDs = slices.Compact(wsIDs)
	var known []domain.WorkspaceID
	for _, ws := range wsIDs {
		if _, err := tx.Workspace(ctx, ws); err == nil {
			f.AddWorkspace(ws)
			known = append(known, ws)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
	}
	switch subj.Kind {
	case domain.SubjectUser, domain.SubjectAgentRun:
		ms, err := tx.MembershipsOf(ctx, subj.UserID, known)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			f.AddMembership(m)
		}
		os, err := tx.OverridesOf(ctx, subj.UserID, noteIDs)
		if err != nil {
			return nil, err
		}
		for _, o := range os {
			f.AddOverride(o)
		}
	case domain.SubjectShareLink:
		l, err := tx.ShareLink(ctx, subj.ShareLinkID)
		if err == nil {
			f.AddShareLink(l)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
	}
	return f, nil
}

// CheckInternalCaller guards Authorize and ListReadableNotes. With a token,
// the caller may only ask about themself (an agent run about its own run or
// its user). Without one, the call is refused unless
// AllowUnauthenticatedInternalCalls is set, for event consumers that have no
// user token until services have their own identity.
func (s *Service) CheckInternalCaller(caller *domain.Subject, subject domain.Subject) error {
	if caller == nil {
		if s.cfg.AllowUnauthenticatedInternalCalls {
			return nil
		}
		return domain.ErrUnauthenticated
	}
	if subject.Kind == domain.SubjectShareLink || caller.UserID != subject.UserID {
		return fmt.Errorf("%w: a caller may only authorize itself", domain.ErrPermissionDenied)
	}
	if caller.Kind == domain.SubjectAgentRun && subject.AgentRunID != caller.AgentRunID {
		return fmt.Errorf("%w: an agent run may only authorize itself", domain.ErrPermissionDenied)
	}
	return nil
}

// ValidateSubject checks a subject from a request.
func ValidateSubject(userID, agentRunID string) (domain.Subject, error) {
	user, err := domain.ParseUserID("subject.user_id", userID)
	if err != nil {
		return domain.Subject{}, err
	}
	var run domain.AgentRunID
	if agentRunID != "" {
		if run, err = domain.ParseAgentRunID("subject.agent_run_id", agentRunID); err != nil {
			return domain.Subject{}, err
		}
	}
	return domain.UserSubject(user, run), nil
}

// decisionKey is the cache key of one check. Agent runs share their user's
// entries, because they are decided as the user.
func decisionKey(subj domain.Subject, c domain.Check) string {
	who := "u:" + string(subj.UserID)
	if subj.Kind == domain.SubjectShareLink {
		who = "l:" + string(subj.ShareLinkID)
	}
	return "authz:v1:" + who + ":" + strconv.Itoa(int(c.Action)) + ":" + c.ResourceID
}

// decisionTags are the invalidation tags of one decided check.
func decisionTags(subj domain.Subject, c domain.Check, f *domain.Facts) []string {
	var tags []string
	if subj.UserID != "" {
		tags = append(tags, domain.UserTag(subj.UserID))
	}
	switch c.Action.Resource() {
	case domain.ResourceNote:
		tags = append(tags, domain.NoteTag(domain.NoteID(c.ResourceID)))
		if n, ok := f.Note(domain.NoteID(c.ResourceID)); ok {
			tags = append(tags, domain.WorkspaceTag(n.WorkspaceID))
		}
	case domain.ResourceWorkspace:
		tags = append(tags, domain.WorkspaceTag(domain.WorkspaceID(c.ResourceID)))
	}
	return tags
}

// ruleFromCache marks decisions answered by the cache.
const ruleFromCache = "cache"

// Authorize answers a batch of checks for subject, one decision per check
// in order. Cached answers are used where present; the rest are decided in
// one read transaction and cached.
func (s *Service) Authorize(ctx context.Context, subj domain.Subject, checks []domain.Check) ([]domain.Decision, error) {
	if len(checks) > s.cfg.MaxChecksPerAuthorize {
		return nil, domain.InvalidField("checks", fmt.Sprintf("at most %d per call", s.cfg.MaxChecksPerAuthorize))
	}
	out := make([]domain.Decision, len(checks))
	var missing []int
	for i, c := range checks {
		if s.cache != nil {
			allowed, found, err := s.cache.Get(ctx, decisionKey(subj, c))
			if err != nil {
				s.log.WarnContext(ctx, "decision cache read failed", "error", err)
			}
			s.metrics.lookup(ctx, found)
			if found {
				out[i] = domain.Decision{Allowed: allowed, Rule: ruleFromCache}
				continue
			}
		}
		missing = append(missing, i)
	}
	if len(missing) == 0 {
		return out, nil
	}
	pending := make([]domain.Check, len(missing))
	for j, i := range missing {
		pending[j] = checks[i]
	}
	now := s.now()
	var facts *domain.Facts
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		facts, err = loadFacts(ctx, tx, subj, pending)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, i := range missing {
		d := domain.Decide(facts, subj, checks[i], now)
		out[i] = d
		if s.cache != nil && !d.IsUnknownResource() {
			if err := s.cache.Set(ctx, decisionKey(subj, checks[i]), d.Allowed, decisionTags(subj, checks[i], facts)...); err != nil {
				s.log.WarnContext(ctx, "decision cache write failed", "error", err)
			}
		}
	}
	return out, nil
}

// ListReadableNotes returns the IDs of the workspace's notes the subject
// can read, ascending. It is not cached: search asks once per query and the
// answer is a list, not a decision.
func (s *Service) ListReadableNotes(ctx context.Context, subj domain.Subject, ws domain.WorkspaceID) ([]domain.NoteID, error) {
	now := s.now()
	var out []domain.NoteID
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.Workspace(ctx, ws); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return nil
			}
			return err
		}
		f := domain.NewFacts()
		f.AddWorkspace(ws)
		notes, err := tx.NotesInWorkspace(ctx, ws)
		if err != nil {
			return err
		}
		for _, n := range notes {
			f.AddNote(n)
		}
		switch subj.Kind {
		case domain.SubjectUser, domain.SubjectAgentRun:
			ms, err := tx.MembershipsOf(ctx, subj.UserID, []domain.WorkspaceID{ws})
			if err != nil {
				return err
			}
			for _, m := range ms {
				f.AddMembership(m)
			}
			os, err := tx.OverridesInWorkspace(ctx, ws, subj.UserID)
			if err != nil {
				return err
			}
			for _, o := range os {
				f.AddOverride(o)
			}
		case domain.SubjectShareLink:
			if l, err := tx.ShareLink(ctx, subj.ShareLinkID); err == nil {
				f.AddShareLink(l)
			}
		}
		out = domain.ReadableNotes(f, subj, notes, now)
		return nil
	})
	return out, err
}

// IssueDelegatedToken signs a token for an agent run acting as the user
// (ADR-0009). The caller must be that user, signed in directly: a
// delegated token cannot mint further delegated tokens, and nobody can mint
// one for someone else. The token carries the user's identity, so it has
// exactly the user's permissions. Its lifetime is the requested expiry,
// capped at DelegatedTokenMaxTTL (the cap when none is requested).
func (s *Service) IssueDelegatedToken(ctx context.Context, caller domain.Subject, user domain.UserID, run domain.AgentRunID, expiresAt *time.Time) (string, time.Time, error) {
	if caller.Kind != domain.SubjectUser || caller.UserID == "" {
		return "", time.Time{}, fmt.Errorf("%w: delegated tokens are issued to the user, signed in directly", domain.ErrPermissionDenied)
	}
	if caller.UserID != user {
		return "", time.Time{}, fmt.Errorf("%w: a user may only delegate their own access", domain.ErrPermissionDenied)
	}
	now := s.now()
	ttl := s.cfg.DelegatedTokenMaxTTL
	if expiresAt != nil {
		if !expiresAt.After(now) {
			return "", time.Time{}, domain.InvalidField("expires_at", "must be in the future")
		}
		ttl = min(ttl, expiresAt.Sub(now))
	}
	// JWT times have one-second resolution; a sub-second lifetime would
	// truncate to an already-expired token.
	ttl = max(ttl.Truncate(time.Second), time.Second)
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		_, err := tx.UserByID(ctx, user)
		return err
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return s.issuer.Issue(domain.UserSubject(user, run), ttl)
}

// NoteEventKind tags the documents events identity projects.
type NoteEventKind int

// Note event kinds.
const (
	NoteCreated NoteEventKind = iota + 1
	NoteTrashed
	NoteRestored
	NotePurged
)

// NoteEvent is a documents event, decoded by the Pub/Sub adapter.
type NoteEvent struct {
	Kind        NoteEventKind
	NoteID      domain.NoteID
	WorkspaceID domain.WorkspaceID // NoteCreated only
	Actor       domain.UserID      // NoteCreated: the creator
	OccurredAt  time.Time
}

// ConsumeOutcome is how an event was handled, for metrics
// (skein.consumer.outcome).
type ConsumeOutcome string

// Consume outcomes.
const (
	OutcomeApplied   ConsumeOutcome = "applied"
	OutcomeDuplicate ConsumeOutcome = "duplicate"
)

// ApplyNoteEvent updates the note projection from one documents event,
// exactly once per event ID for this consumer. Events about notes the
// projection does not hold (other than NoteCreated) change nothing.
func (s *Service) ApplyNoteEvent(ctx context.Context, consumer, eventID string, ev NoteEvent) (ConsumeOutcome, error) {
	first := false
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		if first, err = tx.MarkEventProcessed(ctx, consumer, eventID); err != nil || !first {
			return err
		}
		switch ev.Kind {
		case NoteCreated:
			return tx.UpsertNote(ctx, domain.NoteAccess{ID: ev.NoteID, WorkspaceID: ev.WorkspaceID, CreatedBy: ev.Actor, CreatedAt: ev.OccurredAt})
		case NoteTrashed:
			at := ev.OccurredAt
			return ignoreNotFound(tx.SetNoteTrashed(ctx, ev.NoteID, &at))
		case NoteRestored:
			return ignoreNotFound(tx.SetNoteTrashed(ctx, ev.NoteID, nil))
		case NotePurged:
			return tx.PurgeNote(ctx, ev.NoteID)
		}
		return fmt.Errorf("unknown note event kind %d", ev.Kind)
	})
	if err != nil {
		return "", err
	}
	if !first {
		return OutcomeDuplicate, nil
	}
	s.invalidate(ctx, "event", domain.NoteTag(ev.NoteID))
	return OutcomeApplied, nil
}

// RegisterNote records a note documents has just created, before its
// NoteCreated event reaches the projection, so the creator (and everyone in
// the workspace) can use it at once: without it, every access check in the
// first moments after creation denied the note as unknown. The caller must
// be allowed to create notes in the workspace and is recorded as the
// creator. It only ever inserts: a note already known in the same workspace
// is left as it is (the later NoteCreated agrees with it), and one known in
// another workspace is ErrAlreadyExists, so a caller cannot move a note.
func (s *Service) RegisterNote(ctx context.Context, caller domain.Subject, note domain.NoteID, ws domain.WorkspaceID) error {
	if err := requireUser(caller); err != nil {
		return err
	}
	now := s.now()
	inserted := false
	err := s.store.InTx(ctx, func(ctx context.Context, tx Tx) error {
		create := domain.Check{Action: domain.ActionNoteCreate, ResourceID: string(ws)}
		facts, err := loadFacts(ctx, tx, caller, []domain.Check{create})
		if err != nil {
			return err
		}
		if !domain.Decide(facts, caller, create, now).Allowed {
			return fmt.Errorf("%w: may not create notes in this workspace", domain.ErrPermissionDenied)
		}
		known, err := tx.Notes(ctx, []domain.NoteID{note})
		if err != nil {
			return err
		}
		if len(known) > 0 {
			if known[0].WorkspaceID != ws {
				return fmt.Errorf("%w: note %s belongs to another workspace", domain.ErrAlreadyExists, note)
			}
			return nil
		}
		inserted = true
		return tx.UpsertNote(ctx, domain.NoteAccess{ID: note, WorkspaceID: ws, CreatedBy: caller.UserID, CreatedAt: now})
	})
	if err != nil {
		return err
	}
	if inserted {
		s.invalidate(ctx, "register_note", domain.NoteTag(note))
	}
	return nil
}

func ignoreNotFound(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	return err
}
