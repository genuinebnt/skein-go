// Package policy is the access-control policy the seed data's ground truth
// assumes (ADR-0008), written out as one small evaluator. The identity
// service must implement the same rules, or the permission matrix in
// truth/permissions will flag every difference.
//
// # Rules
//
// Workspace roles: owner > admin > member > guest. Note roles: editor >
// commenter > viewer. A user's role on a note is decided in this order:
//
//  1. Not a member of the note's workspace at time t (never joined, or
//     removed at or before t): no access. Overrides of a removed member are
//     void.
//  2. Owner or admin: editor on every note; overrides are ignored.
//  3. Member: editor, unless a per-note override created at or before t
//     says otherwise (overrides can lower a member, never raise past
//     editor).
//  4. Guest: no access unless an override grants a role.
//
// A share link is presented without signing in. It grants its role on its
// own note while created_at <= t < expires_at (no expiry: forever) and it is
// not revoked at or before t.
//
// From the moment a note is trashed it is visible only to owners and admins
// (read, and delete to restore or purge); everyone else is denied.
//
// Actions need:
//
//	ACTION_NOTE_READ              viewer or higher
//	ACTION_NOTE_COMMENT           commenter or higher
//	ACTION_NOTE_EDIT              editor
//	ACTION_NOTE_DELETE            owner or admin, or the note's creator while an editor
//	ACTION_NOTE_SHARE             editor and at least member (guests cannot share)
//	ACTION_NOTE_CREATE            (workspace) member or higher
//	ACTION_WORKSPACE_MANAGE_MEMBERS  (workspace) admin or higher
//	ACTION_WORKSPACE_MANAGE_SETTINGS (workspace) owner
//	ACTION_AGENT_RUN              (workspace) member or higher
//
// Share-link subjects can only read, comment or edit, per the link's role.
// An agent run acting for a user (ADR-0009) is decided exactly as that user.
//
// # Porting notes
//
// Decisions carry a rule string naming the deciding rule, so a mismatch in a
// service test says which rule the service disagrees with.
package policy

import (
	"time"
)

// WorkspaceRole is a role in a workspace. Values match the proto enum.
type WorkspaceRole int

// Workspace roles.
const (
	Guest  WorkspaceRole = 1
	Member WorkspaceRole = 2
	Admin  WorkspaceRole = 3
	Owner  WorkspaceRole = 4
)

// String returns the role's name as used in the seed files.
func (r WorkspaceRole) String() string {
	switch r {
	case Guest:
		return "guest"
	case Member:
		return "member"
	case Admin:
		return "admin"
	case Owner:
		return "owner"
	default:
		return "none"
	}
}

// ParseWorkspaceRole is the inverse of WorkspaceRole.String.
func ParseWorkspaceRole(s string) WorkspaceRole {
	for _, r := range []WorkspaceRole{Guest, Member, Admin, Owner} {
		if r.String() == s {
			return r
		}
	}
	return 0
}

// NoteRole is a role on a note. Values match the proto enum; 0 is no access.
type NoteRole int

// Note roles.
const (
	NoAccess  NoteRole = 0
	Viewer    NoteRole = 1
	Commenter NoteRole = 2
	Editor    NoteRole = 3
)

// String returns the role's name as used in the seed files.
func (r NoteRole) String() string {
	switch r {
	case Viewer:
		return "viewer"
	case Commenter:
		return "commenter"
	case Editor:
		return "editor"
	default:
		return "none"
	}
}

// ParseNoteRole is the inverse of NoteRole.String.
func ParseNoteRole(s string) NoteRole {
	for _, r := range []NoteRole{Viewer, Commenter, Editor} {
		if r.String() == s {
			return r
		}
	}
	return NoAccess
}

// Action is an authorizable action; values are the proto enum names.
type Action string

// Actions.
const (
	NoteRead       Action = "ACTION_NOTE_READ"
	NoteComment    Action = "ACTION_NOTE_COMMENT"
	NoteEdit       Action = "ACTION_NOTE_EDIT"
	NoteCreate     Action = "ACTION_NOTE_CREATE"
	NoteDelete     Action = "ACTION_NOTE_DELETE"
	NoteShare      Action = "ACTION_NOTE_SHARE"
	ManageMembers  Action = "ACTION_WORKSPACE_MANAGE_MEMBERS"
	ManageSettings Action = "ACTION_WORKSPACE_MANAGE_SETTINGS"
	AgentRun       Action = "ACTION_AGENT_RUN"
)

// NoteActions are the actions whose resource is a note.
var NoteActions = []Action{NoteRead, NoteComment, NoteEdit, NoteDelete, NoteShare}

// WorkspaceActions are the actions whose resource is a workspace.
var WorkspaceActions = []Action{NoteCreate, ManageMembers, ManageSettings, AgentRun}

// Membership is one user's membership of one workspace.
type Membership struct {
	WorkspaceID string
	UserID      string
	Role        WorkspaceRole
	JoinedAt    time.Time
	RemovedAt   *time.Time
}

// Override is a per-note role for one user.
type Override struct {
	NoteID    string
	UserID    string
	Role      NoteRole
	CreatedAt time.Time
}

// ShareLink grants a role on one note to whoever holds its token.
type ShareLink struct {
	ID        string
	NoteID    string
	Role      NoteRole
	CreatedAt time.Time
	ExpiresAt *time.Time
	RevokedAt *time.Time
}

// Note is the part of a note the policy needs.
type Note struct {
	ID          string
	WorkspaceID string
	CreatedBy   string
	// TrashedAt is when the note was moved to the trash, or nil.
	TrashedAt *time.Time
}

func (n Note) trashedAt(t time.Time) bool { return n.TrashedAt != nil && !n.TrashedAt.After(t) }

// Subject is who is asking: a user (possibly through an agent run) or the
// holder of a share link.
type Subject struct {
	UserID      string
	AgentRunID  string
	ShareLinkID string
}

// Decision is the outcome of one check.
type Decision struct {
	Allowed bool
	// Rule names the rule that decided, for example "workspace_role:member"
	// or "share_link:expired".
	Rule string
}

// State is everything the policy reads. Build it with NewState and the Add
// methods; it is read-only afterwards.
type State struct {
	notes       map[string]Note
	workspaces  map[string]bool
	memberships map[string][]Membership // key workspace + "/" + user
	overrides   map[string]Override     // key note + "/" + user
	links       map[string]ShareLink
}

// NewState returns an empty state.
func NewState() *State {
	return &State{
		notes:       map[string]Note{},
		workspaces:  map[string]bool{},
		memberships: map[string][]Membership{},
		overrides:   map[string]Override{},
		links:       map[string]ShareLink{},
	}
}

// AddWorkspace records a workspace ID.
func (s *State) AddWorkspace(id string) { s.workspaces[id] = true }

// AddNote records a note.
func (s *State) AddNote(n Note) { s.notes[n.ID] = n }

// AddMembership records a membership. A user may have several over time.
func (s *State) AddMembership(m Membership) {
	k := m.WorkspaceID + "/" + m.UserID
	s.memberships[k] = append(s.memberships[k], m)
}

// AddOverride records a per-note override (one per note and user).
func (s *State) AddOverride(o Override) { s.overrides[o.NoteID+"/"+o.UserID] = o }

// AddShareLink records a share link.
func (s *State) AddShareLink(l ShareLink) { s.links[l.ID] = l }

// WorkspaceRoleAt returns the user's role in the workspace at time t, or 0.
func (s *State) WorkspaceRoleAt(workspaceID, userID string, t time.Time) WorkspaceRole {
	for _, m := range s.memberships[workspaceID+"/"+userID] {
		if !m.JoinedAt.After(t) && (m.RemovedAt == nil || m.RemovedAt.After(t)) {
			return m.Role
		}
	}
	return 0
}

// RemovedAt returns when the user's most recent membership of the workspace
// ended, or nil if it has not.
func (s *State) RemovedAt(workspaceID, userID string) *time.Time {
	var out *time.Time
	for _, m := range s.memberships[workspaceID+"/"+userID] {
		if m.RemovedAt != nil && (out == nil || m.RemovedAt.After(*out)) {
			out = m.RemovedAt
		}
	}
	return out
}

// WasEverMember reports whether the user ever had a membership of the
// workspace.
func (s *State) WasEverMember(workspaceID, userID string) bool {
	return len(s.memberships[workspaceID+"/"+userID]) > 0
}

// wasRemoved reports whether the user had a membership that ended at or
// before t.
func (s *State) wasRemoved(workspaceID, userID string, t time.Time) bool {
	for _, m := range s.memberships[workspaceID+"/"+userID] {
		if m.RemovedAt != nil && !m.RemovedAt.After(t) {
			return true
		}
	}
	return false
}

// NoteRoleAt returns the user's effective role on a note at time t and the
// rule that decided it.
func (s *State) NoteRoleAt(noteID, userID string, t time.Time) (NoteRole, WorkspaceRole, string) {
	n, ok := s.notes[noteID]
	if !ok {
		return NoAccess, 0, "note:unknown"
	}
	ws := s.WorkspaceRoleAt(n.WorkspaceID, userID, t)
	if ws == 0 {
		if s.wasRemoved(n.WorkspaceID, userID, t) {
			return NoAccess, 0, "membership_removed"
		}
		return NoAccess, 0, "not_member"
	}
	if ws >= Admin {
		return Editor, ws, "workspace_role:" + ws.String()
	}
	if o, ok := s.overrides[noteID+"/"+userID]; ok && !o.CreatedAt.After(t) {
		return o.Role, ws, "note_override:" + o.Role.String()
	}
	if ws == Member {
		return Editor, ws, "workspace_role:member"
	}
	return NoAccess, ws, "guest_without_grant"
}

func roleAllows(role NoteRole, a Action) bool {
	switch a {
	case NoteRead:
		return role >= Viewer
	case NoteComment:
		return role >= Commenter
	case NoteEdit:
		return role >= Editor
	}
	return false
}

// Evaluate decides whether subject may perform action on resource (a note ID
// or a workspace ID, depending on the action) at time t.
func (s *State) Evaluate(subj Subject, a Action, resource string, t time.Time) Decision {
	switch a {
	case NoteCreate, ManageMembers, ManageSettings, AgentRun:
		return s.evaluateWorkspace(subj, a, resource, t)
	}
	n, ok := s.notes[resource]
	if !ok {
		return Decision{Rule: "note:unknown"}
	}
	if subj.ShareLinkID != "" {
		return s.evaluateLink(subj.ShareLinkID, a, n, t)
	}
	role, ws, rule := s.NoteRoleAt(n.ID, subj.UserID, t)
	if n.trashedAt(t) {
		if ws >= Admin && (a == NoteRead || a == NoteDelete) {
			return Decision{Allowed: true, Rule: "trashed:" + ws.String()}
		}
		return Decision{Rule: "note:trashed"}
	}
	switch a {
	case NoteRead, NoteComment, NoteEdit:
		return Decision{Allowed: roleAllows(role, a), Rule: rule}
	case NoteDelete:
		if ws >= Admin {
			return Decision{Allowed: true, Rule: rule}
		}
		if role == Editor && n.CreatedBy == subj.UserID {
			return Decision{Allowed: true, Rule: "creator"}
		}
		if role == Editor {
			return Decision{Rule: "delete_needs_creator_or_admin"}
		}
		return Decision{Rule: rule}
	case NoteShare:
		if role == Editor && ws >= Member {
			return Decision{Allowed: true, Rule: rule}
		}
		if role == Editor {
			return Decision{Rule: "guest_cannot_share"}
		}
		return Decision{Rule: rule}
	}
	return Decision{Rule: "action:unknown"}
}

func (s *State) evaluateLink(linkID string, a Action, n Note, t time.Time) Decision {
	l, ok := s.links[linkID]
	switch {
	case !ok:
		return Decision{Rule: "share_link:unknown"}
	case l.NoteID != n.ID:
		return Decision{Rule: "share_link:other_note"}
	case l.CreatedAt.After(t):
		return Decision{Rule: "share_link:not_yet_created"}
	case l.RevokedAt != nil && !l.RevokedAt.After(t):
		return Decision{Rule: "share_link:revoked"}
	case l.ExpiresAt != nil && !l.ExpiresAt.After(t):
		return Decision{Rule: "share_link:expired"}
	case n.trashedAt(t):
		return Decision{Rule: "note:trashed"}
	}
	switch a {
	case NoteRead, NoteComment, NoteEdit:
		return Decision{Allowed: roleAllows(l.Role, a), Rule: "share_link:" + l.Role.String()}
	}
	return Decision{Rule: "share_link:cannot_manage"}
}

func (s *State) evaluateWorkspace(subj Subject, a Action, workspaceID string, t time.Time) Decision {
	if !s.workspaces[workspaceID] {
		return Decision{Rule: "workspace:unknown"}
	}
	if subj.ShareLinkID != "" {
		return Decision{Rule: "share_link:cannot_manage"}
	}
	ws := s.WorkspaceRoleAt(workspaceID, subj.UserID, t)
	if ws == 0 {
		if s.wasRemoved(workspaceID, subj.UserID, t) {
			return Decision{Rule: "membership_removed"}
		}
		return Decision{Rule: "not_member"}
	}
	need := map[Action]WorkspaceRole{NoteCreate: Member, ManageMembers: Admin, ManageSettings: Owner, AgentRun: Member}[a]
	return Decision{Allowed: ws >= need, Rule: "workspace_role:" + ws.String()}
}
