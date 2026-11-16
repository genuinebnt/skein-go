package app

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

// unknownNoteTitle stands in when the note projection has not seen the
// note (its NoteCreated has not arrived, or predates the subscription).
const unknownNoteTitle = "a note"

// Request handles a NotificationRequested from notifications.requests:
// another service decided who to notify, and this service only stores and
// delivers it. A request located in a note the recipient cannot read is
// dropped, so no service can leak a note's title through a notification.
func (p *Producer) Request(ctx context.Context, ev Event, r domain.Request) (Outcome, error) {
	r.EventID, r.At = ev.ID, ev.OccurredAt
	if r.ActorID == "" {
		r.ActorID = ev.ActorUserID
	}
	candidates, err := domain.RequestCandidates(r)
	if err != nil {
		return "", err
	}
	return p.apply(ctx, ev, pairsOf(candidates), fixed(candidates))
}

// AgentEvent is a NotificationRequested published by the agent service on
// agent.events.
type AgentEvent struct {
	RecipientID  string
	Title        string
	Body         string
	Location     domain.Location
	Confirmation bool
}

// AgentEvent notifies the user an agent run acted for that it finished or
// needs a confirmation. The event's actor must be the run, and the
// recipient its user: an agent acts as its user (ADR-0009) and may notify
// nobody else.
func (p *Producer) AgentEvent(ctx context.Context, ev Event, a AgentEvent) (Outcome, error) {
	if ev.ActorAgentRunID == "" || ev.ActorUserID == "" {
		return "", fmt.Errorf("%w: agent event without an agent run actor", ErrForbidden)
	}
	if a.RecipientID != ev.ActorUserID {
		return "", fmt.Errorf("%w: agent run may only notify its own user", ErrForbidden)
	}
	if err := a.Location.Validate(); err != nil {
		return "", fmt.Errorf("%w: %w", domain.ErrInvalidArgument, err)
	}
	candidates := domain.AgentCandidates(domain.AgentRun{
		EventID: ev.ID, RunID: ev.ActorAgentRunID, UserID: ev.ActorUserID, Title: a.Title, Body: a.Body,
		Location: a.Location, Confirmation: a.Confirmation, At: ev.OccurredAt,
	})
	return p.apply(ctx, ev, pairsOf(candidates), fixed(candidates))
}

// CommentEvent is a new comment. events.proto has no comment payload yet,
// so today only the seed loader produces these; the proposed CommentAdded
// message is in the service README.
type CommentEvent struct {
	CommentID string
	NoteID    string
	// ParentAuthorID is the replied-to comment's author, empty for a new
	// thread.
	ParentAuthorID string
	Mentions       []string
	Body           string
	Location       domain.Location
}

// Comment applies the comment rules. The author is the event's actor.
func (p *Producer) Comment(ctx context.Context, ev Event, c CommentEvent) (Outcome, error) {
	note, found, err := p.note(ctx, c.NoteID)
	if err != nil {
		return "", err
	}
	if !found {
		note = NoteInfo{ID: c.NoteID, Title: unknownNoteTitle}
		p.cfg.Logger.WarnContext(ctx, "comment on a note the projection does not know",
			slog.String("event_id", ev.ID), slog.String("note_id", c.NoteID))
	}
	candidates := domain.CommentCandidates(domain.Comment{
		ID: c.CommentID, NoteID: c.NoteID, NoteTitle: note.Title, NoteCreatorID: note.CreatedBy,
		AuthorID: ev.ActorUserID, AuthorName: p.displayName(ctx, ev.ActorUserID), ParentAuthorID: c.ParentAuthorID,
		Mentions: c.Mentions, Body: c.Body, Location: c.Location, At: ev.OccurredAt,
	})
	return p.apply(ctx, ev, pairsOf(candidates), fixed(candidates))
}

// AccessChangedEvent is identity's AccessChanged.
type AccessChangedEvent struct {
	WorkspaceID string
	NoteID      string
	UserID      string
	Revoked     bool
}

// AccessChanged applies the access rules. For a note-level change it asks
// identity, at the event's time, whether the user is a member of the
// workspace (a guest's new role is a share) and what role they now hold.
func (p *Producer) AccessChanged(ctx context.Context, ev Event, a AccessChangedEvent) (Outcome, error) {
	change := domain.AccessChange{WorkspaceID: a.WorkspaceID, NoteID: a.NoteID, UserID: a.UserID,
		ActorID: ev.ActorUserID, Revoked: a.Revoked, At: ev.OccurredAt}
	if a.NoteID != "" && a.UserID != ev.ActorUserID {
		note, found, err := p.note(ctx, a.NoteID)
		if err != nil {
			return "", err
		}
		change.NoteTitle = note.Title
		if !found {
			change.NoteTitle = unknownNoteTitle
		}
		workspace := a.WorkspaceID
		if workspace == "" {
			workspace = note.WorkspaceID
		}
		checks := []Check{
			{ActionNoteCreate, workspace},
			{ActionNoteRead, a.NoteID}, {ActionNoteComment, a.NoteID}, {ActionNoteEdit, a.NoteID},
		}
		allowed, err := p.cfg.Authorizer.Authorize(ctx, a.UserID, checks, ev.OccurredAt)
		if err != nil {
			return "", fmt.Errorf("app: authorize: %w", err)
		}
		if len(allowed) != len(checks) {
			return "", fmt.Errorf("app: authorize returned %d answers for %d checks", len(allowed), len(checks))
		}
		change.RecipientIsMember = workspace != "" && allowed[0]
		switch {
		case allowed[3]:
			change.Role = "editor"
		case allowed[2]:
			change.Role = "commenter"
		case allowed[1]:
			change.Role = "viewer"
		}
		change.ActorName = p.displayName(ctx, ev.ActorUserID)
	}
	candidates := domain.AccessCandidates(change)
	return p.apply(ctx, ev, nil, fixed(candidates))
}

// NoteCreated records a note in the projection; its creator is the actor.
func (p *Producer) NoteCreated(ctx context.Context, ev Event, noteID, workspaceID, title string) (Outcome, error) {
	return p.apply(ctx, ev, nil, func(ctx context.Context, tx Tx, _ *placement) ([]domain.Candidate, error) {
		return nil, tx.SaveNote(ctx, NoteInfo{ID: noteID, WorkspaceID: workspaceID, Title: title, CreatedBy: ev.ActorUserID})
	})
}

// NoteRenamed updates a known note's title; an unknown note is ignored.
func (p *Producer) NoteRenamed(ctx context.Context, ev Event, noteID, title string) (Outcome, error) {
	return p.updateNote(ctx, ev, noteID, func(n *NoteInfo) { n.Title = title })
}

// NoteTrashed and NoteRestored keep the projection's flag current. They
// notify nobody: there is no notification kind for it, and once a note is
// trashed only owners and admins can read it, so the read check already
// stops new mentions and comments from reaching anyone else.
func (p *Producer) NoteTrashed(ctx context.Context, ev Event, noteID string) (Outcome, error) {
	return p.updateNote(ctx, ev, noteID, func(n *NoteInfo) { n.Trashed = true })
}

// NoteRestored clears the trashed flag.
func (p *Producer) NoteRestored(ctx context.Context, ev Event, noteID string) (Outcome, error) {
	return p.updateNote(ctx, ev, noteID, func(n *NoteInfo) { n.Trashed = false })
}

func (p *Producer) updateNote(ctx context.Context, ev Event, noteID string, change func(*NoteInfo)) (Outcome, error) {
	return p.apply(ctx, ev, nil, func(ctx context.Context, tx Tx, _ *placement) ([]domain.Candidate, error) {
		note, found, err := tx.Note(ctx, noteID)
		if err != nil || !found {
			return nil, err
		}
		change(&note)
		return nil, tx.SaveNote(ctx, note)
	})
}

// NotePurged erases everything this service holds about the note,
// including the notifications located in it, whose bodies quote its text
// (events.proto: "every projection must erase what it holds").
func (p *Producer) NotePurged(ctx context.Context, ev Event, noteID string) (Outcome, error) {
	return p.apply(ctx, ev, nil, func(ctx context.Context, tx Tx, pl *placement) ([]domain.Candidate, error) {
		recipients, err := tx.PurgeNote(ctx, noteID)
		pl.changed(recipients...)
		return nil, err
	})
}

// ChangedBlock is one block of a BlocksChanged event.
type ChangedBlock struct {
	ID   string
	Kind string
	Text string
}

// BlocksChangedEvent is documents' BlocksChanged.
type BlocksChangedEvent struct {
	NoteID          string
	Blocks          []ChangedBlock
	RemovedBlockIDs []string
}

// BlocksChanged notifies users newly @mentioned in the changed blocks.
//
// The projection keeps, per block, the users its text mentions. An edit
// notifies only the users that were not mentioned in that block before,
// so typing around an existing mention, or ten edits of one sentence,
// notify once; a mention deleted and typed again notifies again, and then
// the coalescing window folds it into the earlier notification if that is
// still unread. Handles resolve against the note's workspace members, and
// a mentioned user who cannot read the note gets nothing.
func (p *Producer) BlocksChanged(ctx context.Context, ev Event, b BlocksChangedEvent) (Outcome, error) {
	note, found, err := p.note(ctx, b.NoteID)
	if err != nil {
		return "", err
	}
	if !found {
		p.cfg.Logger.WarnContext(ctx, "blocks changed on a note the projection does not know; mentions skipped",
			slog.String("event_id", ev.ID), slog.String("note_id", b.NoteID))
		return p.apply(ctx, ev, nil, fixed(nil))
	}

	handlesByBlock := make([][]string, len(b.Blocks))
	var allHandles []string
	for i, blk := range b.Blocks {
		if blk.Kind == domain.BlockKindCode {
			continue
		}
		handlesByBlock[i] = domain.ExtractHandles(blk.Text)
		allHandles = append(allHandles, handlesByBlock[i]...)
	}
	allHandles = slices.Compact(slices.Sorted(slices.Values(allHandles)))
	resolved := map[string]string{}
	if len(allHandles) > 0 {
		if resolved, err = p.cfg.Directory.ResolveHandles(ctx, note.WorkspaceID, allHandles); err != nil {
			return "", fmt.Errorf("app: resolve handles: %w", err)
		}
	}

	usersByBlock := make([][]string, len(b.Blocks))
	handleOf := map[string]string{}
	var pairs []readPair
	for i, handles := range handlesByBlock {
		for _, h := range handles {
			if u, ok := resolved[h]; ok {
				usersByBlock[i] = append(usersByBlock[i], u)
				if _, seen := handleOf[u]; !seen {
					handleOf[u] = h
					pairs = append(pairs, readPair{u, note.ID})
				}
			}
		}
		usersByBlock[i] = slices.Compact(slices.Sorted(slices.Values(usersByBlock[i])))
	}
	actorName := ""
	if len(handleOf) > 0 {
		actorName = p.displayName(ctx, ev.ActorUserID)
	}

	return p.apply(ctx, ev, pairs, func(ctx context.Context, tx Tx, _ *placement) ([]domain.Candidate, error) {
		ids := make([]string, 0, len(b.Blocks)+len(b.RemovedBlockIDs))
		for _, blk := range b.Blocks {
			ids = append(ids, blk.ID)
		}
		ids = append(ids, b.RemovedBlockIDs...)
		stored, err := tx.BlockMentions(ctx, note.ID, ids)
		if err != nil {
			return nil, err
		}
		previous := maps.Clone(stored)
		if previous == nil {
			previous = map[string][]string{}
		}
		mentions := domain.BlockMentions{EventID: ev.ID, NoteID: note.ID, NoteTitle: note.Title,
			ActorID: ev.ActorUserID, ActorName: actorName, At: ev.OccurredAt}
		for i, blk := range b.Blocks {
			current := usersByBlock[i]
			if slices.Equal(previous[blk.ID], current) {
				continue
			}
			if err := tx.SetBlockMentions(ctx, note.ID, blk.ID, current); err != nil {
				return nil, err
			}
			mb := domain.MentionedBlock{BlockID: blk.ID, Text: blk.Text}
			for _, u := range domain.NewlyMentioned(previous[blk.ID], current) {
				mb.Users = append(mb.Users, domain.MentionedUser{UserID: u, Handle: handleOf[u]})
			}
			if len(mb.Users) > 0 {
				mentions.Blocks = append(mentions.Blocks, mb)
			}
			// A later block in the same event must diff against this one's
			// new state if the event lists the block twice.
			previous[blk.ID] = current
		}
		for _, id := range b.RemovedBlockIDs {
			if len(previous[id]) > 0 {
				if err := tx.SetBlockMentions(ctx, note.ID, id, nil); err != nil {
					return nil, err
				}
				previous[id] = nil
			}
		}
		return domain.MentionCandidates(mentions), nil
	})
}
