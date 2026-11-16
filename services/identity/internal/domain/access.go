package domain

import "time"

// Membership is one user's membership of one workspace. Identity keeps one
// row per (workspace, user): removing a member sets RemovedAt, and adding
// them back starts a new membership (new JoinedAt, RemovedAt cleared).
type Membership struct {
	WorkspaceID WorkspaceID
	UserID      UserID
	Role        WorkspaceRole
	JoinedAt    time.Time
	RemovedAt   *time.Time
	InvitedBy   UserID // empty for the creator
}

// ActiveAt reports whether the membership grants its role at time t.
func (m Membership) ActiveAt(t time.Time) bool {
	return !m.JoinedAt.After(t) && (m.RemovedAt == nil || m.RemovedAt.After(t))
}

// RemovedBy reports whether the membership ended at or before t.
func (m Membership) RemovedBy(t time.Time) bool { return m.RemovedAt != nil && !m.RemovedAt.After(t) }

// NoteRoleOverride is a per-note role for one user, replacing the role the
// workspace would give. WorkspaceID is the note's workspace, kept so that
// removing a member can drop their overrides in one statement.
type NoteRoleOverride struct {
	NoteID      NoteID
	WorkspaceID WorkspaceID
	UserID      UserID
	Role        NoteRole
	GrantedBy   UserID
	CreatedAt   time.Time
}

// ShareLink grants a role on one note to whoever presents its token. Only
// the token's SHA-256 is stored; the token itself is shown once.
type ShareLink struct {
	ID          ShareLinkID
	NoteID      NoteID
	WorkspaceID WorkspaceID
	Role        NoteRole
	TokenHash   string
	CreatedBy   UserID
	CreatedAt   time.Time
	ExpiresAt   *time.Time // nil: never expires
	RevokedAt   *time.Time
}

// NoteAccess is identity's projection of a note: the fields the policy
// needs, maintained from documents' events (NoteCreated, NoteTrashed,
// NoteRestored, NotePurged).
type NoteAccess struct {
	ID          NoteID
	WorkspaceID WorkspaceID
	CreatedBy   UserID
	CreatedAt   time.Time
	TrashedAt   *time.Time
}

// TrashedBy reports whether the note is in the trash at time t.
func (n NoteAccess) TrashedBy(t time.Time) bool { return n.TrashedAt != nil && !n.TrashedAt.After(t) }

// SubjectKind tags who is acting.
type SubjectKind int

// Subject kinds.
const (
	// SubjectUser is a signed-in user.
	SubjectUser SubjectKind = iota + 1
	// SubjectAgentRun is an agent run acting for UserID; decided as the user.
	SubjectAgentRun
	// SubjectShareLink is whoever holds a share link's token.
	SubjectShareLink
)

// Subject is who is asking.
type Subject struct {
	Kind        SubjectKind
	UserID      UserID      // SubjectUser, SubjectAgentRun
	AgentRunID  AgentRunID  // SubjectAgentRun
	ShareLinkID ShareLinkID // SubjectShareLink
}

// UserSubject returns a subject for a user, or for an agent run acting for
// that user when agentRun is not empty.
func UserSubject(user UserID, agentRun AgentRunID) Subject {
	if agentRun != "" {
		return Subject{Kind: SubjectAgentRun, UserID: user, AgentRunID: agentRun}
	}
	return Subject{Kind: SubjectUser, UserID: user}
}

// LinkSubject returns a subject for a share link holder.
func LinkSubject(link ShareLinkID) Subject {
	return Subject{Kind: SubjectShareLink, ShareLinkID: link}
}

// Check is one (action, resource) pair to authorize. ResourceID is a note
// ID or a workspace ID, according to Action.Resource.
type Check struct {
	Action     Action
	ResourceID string
}

// Decision is the outcome of one check and the rule that decided it.
type Decision struct {
	Allowed bool
	Rule    string
}

// Facts are the rows a batch of checks can read. Build with NewFacts and the
// Add methods; Decide only reads it. Keys are lookups, never iterated, so
// results never depend on map order.
type Facts struct {
	workspaces  map[WorkspaceID]bool
	notes       map[NoteID]NoteAccess
	memberships map[membershipKey]Membership
	overrides   map[overrideKey]NoteRoleOverride
	links       map[ShareLinkID]ShareLink
}

type membershipKey struct {
	workspace WorkspaceID
	user      UserID
}

type overrideKey struct {
	note NoteID
	user UserID
}

// NewFacts returns empty facts.
func NewFacts() *Facts {
	return &Facts{
		workspaces:  map[WorkspaceID]bool{},
		notes:       map[NoteID]NoteAccess{},
		memberships: map[membershipKey]Membership{},
		overrides:   map[overrideKey]NoteRoleOverride{},
		links:       map[ShareLinkID]ShareLink{},
	}
}

// AddWorkspace records that a workspace exists.
func (f *Facts) AddWorkspace(id WorkspaceID) { f.workspaces[id] = true }

// AddNote records a note (and that its workspace exists).
func (f *Facts) AddNote(n NoteAccess) {
	f.notes[n.ID] = n
	f.workspaces[n.WorkspaceID] = true
}

// AddMembership records a membership; a later one for the same pair
// replaces it, matching the one-row-per-pair storage.
func (f *Facts) AddMembership(m Membership) {
	f.memberships[membershipKey{m.WorkspaceID, m.UserID}] = m
}

// AddOverride records a per-note override.
func (f *Facts) AddOverride(o NoteRoleOverride) { f.overrides[overrideKey{o.NoteID, o.UserID}] = o }

// AddShareLink records a share link.
func (f *Facts) AddShareLink(l ShareLink) { f.links[l.ID] = l }

// Note returns a recorded note.
func (f *Facts) Note(id NoteID) (NoteAccess, bool) {
	n, ok := f.notes[id]
	return n, ok
}
