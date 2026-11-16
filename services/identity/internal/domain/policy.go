package domain

import (
	"slices"
	"time"
)

// Decision rules, spelled as in truth/permissions/matrix.jsonl. Role-bearing
// rules are built with rulePrefix + role name.
const (
	ruleNoteUnknown          = "note:unknown"
	ruleWorkspaceUnknown     = "workspace:unknown"
	ruleActionUnknown        = "action:unknown"
	ruleNotMember            = "not_member"
	ruleMembershipRemoved    = "membership_removed"
	ruleGuestWithoutGrant    = "guest_without_grant"
	ruleNoteTrashed          = "note:trashed"
	ruleCreator              = "creator"
	ruleDeleteNeedsCreator   = "delete_needs_creator_or_admin"
	ruleGuestCannotShare     = "guest_cannot_share"
	ruleLinkUnknown          = "share_link:unknown"
	ruleLinkOtherNote        = "share_link:other_note"
	ruleLinkNotYetCreated    = "share_link:not_yet_created"
	ruleLinkRevoked          = "share_link:revoked"
	ruleLinkExpired          = "share_link:expired"
	ruleLinkCannotManage     = "share_link:cannot_manage"
	ruleSubjectInvalid       = "subject:invalid"
	prefixWorkspaceRole      = "workspace_role:"
	prefixNoteOverride       = "note_override:"
	prefixTrashed            = "trashed:"
	prefixShareLink          = "share_link:"
	maxShareLinkGrantedRoles = NoteRoleCommenter
)

// IsUnknownResource reports whether a decision was made because the note or
// workspace is not known. Such decisions are not cached: the projection may
// simply not have caught up with documents yet.
func (d Decision) IsUnknownResource() bool {
	return d.Rule == ruleNoteUnknown || d.Rule == ruleWorkspaceUnknown
}

// WorkspaceRoleAt returns the user's role in the workspace at t, or
// WorkspaceRoleNone, and whether a membership ended at or before t.
func (f *Facts) WorkspaceRoleAt(ws WorkspaceID, user UserID, t time.Time) (WorkspaceRole, bool) {
	m, ok := f.memberships[membershipKey{ws, user}]
	if !ok {
		return WorkspaceRoleNone, false
	}
	if m.ActiveAt(t) {
		return m.Role, false
	}
	return WorkspaceRoleNone, m.RemovedBy(t)
}

// NoteRoleAt returns the user's effective role on a note at t, their
// workspace role, and the rule that decided it (rules 1 to 4).
func (f *Facts) NoteRoleAt(note NoteID, user UserID, t time.Time) (NoteRole, WorkspaceRole, string) {
	n, ok := f.notes[note]
	if !ok {
		return NoteRoleNone, WorkspaceRoleNone, ruleNoteUnknown
	}
	ws, removed := f.WorkspaceRoleAt(n.WorkspaceID, user, t)
	switch {
	case ws == WorkspaceRoleNone && removed:
		return NoteRoleNone, ws, ruleMembershipRemoved
	case ws == WorkspaceRoleNone:
		return NoteRoleNone, ws, ruleNotMember
	case ws >= WorkspaceRoleAdmin:
		return NoteRoleEditor, ws, prefixWorkspaceRole + ws.String()
	}
	if o, ok := f.overrides[overrideKey{note, user}]; ok && !o.CreatedAt.After(t) {
		return o.Role, ws, prefixNoteOverride + o.Role.String()
	}
	if ws == WorkspaceRoleMember {
		return NoteRoleEditor, ws, prefixWorkspaceRole + ws.String()
	}
	return NoteRoleNone, ws, ruleGuestWithoutGrant
}

// roleAllows reports whether a note role is enough for a content action.
func roleAllows(role NoteRole, a Action) bool {
	switch a {
	case ActionNoteRead:
		return role >= NoteRoleViewer
	case ActionNoteComment:
		return role >= NoteRoleCommenter
	case ActionNoteEdit:
		return role >= NoteRoleEditor
	default:
		return false
	}
}

// Decide answers one check for subject at time t. It is a total function:
// unknown actions, resources and subjects are denied with a rule saying so.
func Decide(f *Facts, subj Subject, c Check, t time.Time) Decision {
	switch c.Action.Resource() {
	case ResourceWorkspace:
		return decideWorkspace(f, subj, c.Action, WorkspaceID(c.ResourceID), t)
	case ResourceNote:
	default:
		return Decision{Rule: ruleActionUnknown}
	}
	n, ok := f.notes[NoteID(c.ResourceID)]
	if !ok {
		return Decision{Rule: ruleNoteUnknown}
	}
	switch subj.Kind {
	case SubjectShareLink:
		return decideLink(f, subj.ShareLinkID, c.Action, n, t)
	case SubjectUser, SubjectAgentRun:
		// An agent run is decided exactly as its user (ADR-0009).
	default:
		return Decision{Rule: ruleSubjectInvalid}
	}
	role, ws, rule := f.NoteRoleAt(n.ID, subj.UserID, t)
	if n.TrashedBy(t) {
		if ws >= WorkspaceRoleAdmin && (c.Action == ActionNoteRead || c.Action == ActionNoteDelete) {
			return Decision{Allowed: true, Rule: prefixTrashed + ws.String()}
		}
		return Decision{Rule: ruleNoteTrashed}
	}
	switch c.Action {
	case ActionNoteRead, ActionNoteComment, ActionNoteEdit:
		return Decision{Allowed: roleAllows(role, c.Action), Rule: rule}
	case ActionNoteDelete:
		switch {
		case ws >= WorkspaceRoleAdmin:
			return Decision{Allowed: true, Rule: rule}
		case role == NoteRoleEditor && n.CreatedBy == subj.UserID:
			return Decision{Allowed: true, Rule: ruleCreator}
		case role == NoteRoleEditor:
			return Decision{Rule: ruleDeleteNeedsCreator}
		}
		return Decision{Rule: rule}
	case ActionNoteShare:
		switch {
		case role == NoteRoleEditor && ws >= WorkspaceRoleMember:
			return Decision{Allowed: true, Rule: rule}
		case role == NoteRoleEditor:
			return Decision{Rule: ruleGuestCannotShare}
		}
		return Decision{Rule: rule}
	}
	return Decision{Rule: ruleActionUnknown}
}

// decideLink applies rule 5. Links grant at most commenter (see the package
// documentation), however they were stored.
func decideLink(f *Facts, id ShareLinkID, a Action, n NoteAccess, t time.Time) Decision {
	l, ok := f.links[id]
	switch {
	case !ok:
		return Decision{Rule: ruleLinkUnknown}
	case l.NoteID != n.ID:
		return Decision{Rule: ruleLinkOtherNote}
	case l.CreatedAt.After(t):
		return Decision{Rule: ruleLinkNotYetCreated}
	case l.RevokedAt != nil && !l.RevokedAt.After(t):
		return Decision{Rule: ruleLinkRevoked}
	case l.ExpiresAt != nil && !l.ExpiresAt.After(t):
		return Decision{Rule: ruleLinkExpired}
	case n.TrashedBy(t):
		return Decision{Rule: ruleNoteTrashed}
	}
	switch a {
	case ActionNoteRead, ActionNoteComment, ActionNoteEdit:
		role := min(l.Role, maxShareLinkGrantedRoles)
		return Decision{Allowed: roleAllows(role, a), Rule: prefixShareLink + role.String()}
	}
	return Decision{Rule: ruleLinkCannotManage}
}

// workspaceActionNeeds is the minimum workspace role per workspace action.
func workspaceActionNeeds(a Action) WorkspaceRole {
	switch a {
	case ActionNoteCreate, ActionAgentRun:
		return WorkspaceRoleMember
	case ActionManageMembers:
		return WorkspaceRoleAdmin
	case ActionManageSettings:
		return WorkspaceRoleOwner
	default:
		return WorkspaceRoleOwner + 1 // unreachable: no role suffices
	}
}

func decideWorkspace(f *Facts, subj Subject, a Action, ws WorkspaceID, t time.Time) Decision {
	if !f.workspaces[ws] {
		return Decision{Rule: ruleWorkspaceUnknown}
	}
	switch subj.Kind {
	case SubjectShareLink:
		return Decision{Rule: ruleLinkCannotManage}
	case SubjectUser, SubjectAgentRun:
	default:
		return Decision{Rule: ruleSubjectInvalid}
	}
	role, removed := f.WorkspaceRoleAt(ws, subj.UserID, t)
	switch {
	case role == WorkspaceRoleNone && removed:
		return Decision{Rule: ruleMembershipRemoved}
	case role == WorkspaceRoleNone:
		return Decision{Rule: ruleNotMember}
	}
	return Decision{Allowed: role >= workspaceActionNeeds(a), Rule: prefixWorkspaceRole + role.String()}
}

// Authorize decides a batch of checks, one decision per check in order.
func Authorize(f *Facts, subj Subject, checks []Check, t time.Time) []Decision {
	out := make([]Decision, len(checks))
	for i, c := range checks {
		out[i] = Decide(f, subj, c, t)
	}
	return out
}

// ReadableNotes returns the IDs of the notes, among those given, that
// subject may read at t, sorted ascending (UUIDv7 order is creation order),
// so the result does not depend on how the notes were listed.
func ReadableNotes(f *Facts, subj Subject, notes []NoteAccess, t time.Time) []NoteID {
	out := make([]NoteID, 0, len(notes))
	for _, n := range notes {
		if Decide(f, subj, Check{Action: ActionNoteRead, ResourceID: string(n.ID)}, t).Allowed {
			out = append(out, n.ID)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// EffectiveNoteRole is the note role a user has at t, for deciding whether
// an override change lowered their access (AccessChanged.revoked). Trashing
// is ignored: it is a note state, not an access grant.
func EffectiveNoteRole(f *Facts, note NoteID, user UserID, t time.Time) NoteRole {
	role, _, _ := f.NoteRoleAt(note, user, t)
	return role
}
