package domain

import (
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"time"
)

// BlockKindCode is the ChangedBlock kind of code blocks. Their text is
// source code, so it is never scanned for mentions.
const BlockKindCode = "code"

// Comment is a new comment, as the producer rules need it.
type Comment struct {
	ID            string
	NoteID        string
	NoteTitle     string
	NoteCreatorID string
	AuthorID      string
	AuthorName    string
	// ParentAuthorID is the author of the comment replied to; empty for the
	// first comment of a thread.
	ParentAuthorID string
	// Mentions are the user IDs the comment @mentions, as documents
	// resolved them when the comment was written.
	Mentions []string
	Body     string
	Location Location
	At       time.Time
}

// CommentCandidates applies the comment rules (package documentation):
// MENTION to each mentioned user, then COMMENT to the note's creator and
// the parent's author, never twice to one user for one comment and never
// to the author. Every candidate needs read access to the note.
func CommentCandidates(c Comment) []Candidate {
	notified := map[string]bool{c.AuthorID: true, "": true}
	var out []Candidate
	add := func(user string, kind Kind, title string) {
		if notified[user] {
			return
		}
		notified[user] = true
		out = append(out, Candidate{
			RecipientID: user, Kind: kind, Title: title, Body: c.Body, Location: c.Location,
			ActorID: c.AuthorID, Source: Source{Kind: SourceComment, ID: c.ID}, At: c.At, NeedsRead: c.NoteID,
		})
	}
	for _, u := range c.Mentions {
		add(u, KindMention, fmt.Sprintf("%s mentioned you in %s", c.AuthorName, c.NoteTitle))
	}
	for _, u := range []string{c.NoteCreatorID, c.ParentAuthorID} {
		add(u, KindComment, fmt.Sprintf("%s commented on %s", c.AuthorName, c.NoteTitle))
	}
	return out
}

// AccessChange is identity's AccessChanged event, with the facts the rule
// needs looked up.
type AccessChange struct {
	WorkspaceID string
	// NoteID is empty for a workspace-level change.
	NoteID    string
	NoteTitle string
	UserID    string
	ActorID   string
	ActorName string
	Revoked   bool
	// RecipientIsMember reports whether the user is a member (or above) of
	// the workspace at At. A role given to a guest is a share; a role given
	// to a member changes the access they already had.
	RecipientIsMember bool
	// Role is the user's resulting role on the note ("viewer", "commenter"
	// or "editor"), or empty when they have none.
	Role string
	At   time.Time
}

// AccessCandidates applies the access rules: SHARE to a guest given a role
// on a note, ACCESS_CHANGED to a member whose note role changes, to a user
// whose note access is revoked and to a removed member. Joining a workspace,
// a workspace role change and anything a user does to themselves notify
// nobody.
func AccessCandidates(a AccessChange) []Candidate {
	if a.UserID == "" || a.UserID == a.ActorID {
		return nil
	}
	if a.NoteID == "" {
		if !a.Revoked {
			return nil
		}
		return []Candidate{{
			RecipientID: a.UserID, Kind: KindAccessChanged, Title: "You were removed from a workspace",
			Body: "You no longer have access to this workspace.", ActorID: a.ActorID,
			Source: Source{Kind: SourceMembership, ID: a.WorkspaceID + "/" + a.UserID}, At: a.At,
		}}
	}
	c := Candidate{
		RecipientID: a.UserID, Kind: KindAccessChanged, Title: fmt.Sprintf("Your access to %s changed", a.NoteTitle),
		Location: Location{NoteID: a.NoteID}, ActorID: a.ActorID,
		Source: Source{Kind: SourceNoteRole, ID: a.NoteID + "/" + a.UserID}, At: a.At,
	}
	switch {
	case a.Revoked || a.Role == "":
		c.Body = "You no longer have access to this note."
	case a.RecipientIsMember:
		c.Body = "Role: " + a.Role + "."
	default:
		c.Kind, c.Title, c.Body = KindShare, fmt.Sprintf("%s shared %s with you", a.ActorName, a.NoteTitle), "Role: "+a.Role+"."
	}
	return []Candidate{c}
}

// AgentRun is a finished agent run, or one paused for confirmation.
type AgentRun struct {
	EventID string
	RunID   string
	// UserID is the user the run acts for (ADR-0009), the only recipient
	// an agent may notify.
	UserID       string
	Title        string
	Body         string
	Location     Location
	Confirmation bool
	At           time.Time
}

// AgentCandidates notifies the run's user. A confirmation request is
// AGENT_RUN_FINISHED too until the contract has a kind of its own (see the
// service README); its source is the event, so several confirmations of one
// run each notify, while repeated finishes of one run collapse into one.
func AgentCandidates(r AgentRun) []Candidate {
	c := Candidate{
		RecipientID: r.UserID, Kind: KindAgentRunFinished, Title: r.Title, Body: r.Body,
		Location: r.Location, ActorID: r.UserID, Source: Source{Kind: SourceAgentRun, ID: r.RunID},
		At: r.At, NeedsRead: r.Location.NoteID,
	}
	if r.Confirmation {
		c.Source = Source{Kind: SourceAgentConfirmation, ID: r.EventID}
		c.CoalesceKey = "agent_confirmation/" + r.RunID
		if c.Title == "" {
			c.Title = "The agent needs your confirmation"
		}
	} else if c.Title == "" {
		c.Title = "Agent run finished"
	}
	return []Candidate{c}
}

// Request is a NotificationRequested event from any service.
type Request struct {
	EventID     string
	RecipientID string
	Kind        Kind
	Title       string
	Body        string
	Location    Location
	ActorID     string
	At          time.Time
}

// RequestCandidates validates a request and returns its one candidate.
// Identical requests (same recipient, kind, title and place) within the
// coalescing window collapse into one notification.
func RequestCandidates(r Request) ([]Candidate, error) {
	switch {
	case !ValidUUID(r.RecipientID):
		return nil, fmt.Errorf("%w: recipient is not a UUID", ErrInvalidArgument)
	case !r.Kind.Known():
		return nil, fmt.Errorf("%w: %w: %d", ErrInvalidArgument, ErrUnknownKind, r.Kind)
	case strings.TrimSpace(r.Title) == "":
		return nil, fmt.Errorf("%w: title is empty", ErrInvalidArgument)
	case r.ActorID != "" && !ValidUUID(r.ActorID):
		return nil, fmt.Errorf("%w: actor is not a UUID", ErrInvalidArgument)
	}
	if err := r.Location.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(r.Title))
	key := fmt.Sprintf("request/%d/%s/%s/%016x", r.Kind, r.Location.NoteID, r.Location.BlockID, h.Sum64())
	return []Candidate{{
		RecipientID: r.RecipientID, Kind: r.Kind, Title: r.Title, Body: r.Body, Location: r.Location,
		ActorID: r.ActorID, Source: Source{Kind: SourceRequest, ID: r.EventID}, CoalesceKey: key,
		At: r.At, NeedsRead: r.Location.NoteID,
	}}, nil
}

// MentionedUser is a resolved @handle.
type MentionedUser struct {
	UserID string
	Handle string
}

// MentionedBlock is a changed block and the users newly mentioned in it.
type MentionedBlock struct {
	BlockID string
	Text    string
	Users   []MentionedUser
}

// BlockMentions are the new @mentions one BlocksChanged event introduced.
type BlockMentions struct {
	EventID   string
	NoteID    string
	NoteTitle string
	ActorID   string
	ActorName string
	Blocks    []MentionedBlock
	At        time.Time
}

// MentionCandidates returns one MENTION per newly mentioned user other
// than the actor, located at the first block (in event order) that
// mentions them. The coalescing key is the note and actor, so a burst of
// edits by one person mentioning the same user becomes one notification.
func MentionCandidates(m BlockMentions) []Candidate {
	seen := map[string]bool{m.ActorID: true, "": true}
	var out []Candidate
	for _, b := range m.Blocks {
		for _, u := range b.Users {
			if seen[u.UserID] {
				continue
			}
			seen[u.UserID] = true
			out = append(out, Candidate{
				RecipientID: u.UserID, Kind: KindMention,
				Title:    fmt.Sprintf("%s mentioned you in %s", m.ActorName, m.NoteTitle),
				Body:     Excerpt(b.Text, u.Handle),
				Location: Location{NoteID: m.NoteID, BlockID: b.BlockID}, ActorID: m.ActorID,
				Source:      Source{Kind: SourceBlockMention, ID: m.EventID},
				CoalesceKey: "mention/" + m.NoteID + "/" + m.ActorID, At: m.At, NeedsRead: m.NoteID,
			})
		}
	}
	return out
}

// NewlyMentioned returns the users in current that are not in previous:
// those a block edit has just mentioned. Both are sorted ascending and
// free of duplicates; so is the result.
func NewlyMentioned(previous, current []string) []string {
	var out []string
	for _, u := range current {
		if _, found := slices.BinarySearch(previous, u); !found {
			out = append(out, u)
		}
	}
	return out
}
