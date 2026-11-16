package seed

import (
	"context"
	"time"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
)

// Workspace and note roles, ordered so comparisons read as "at least".
const (
	roleNone      = 0
	roleGuest     = 1
	roleMember    = 2
	roleAdmin     = 3
	roleOwner     = 4
	noteViewer    = 1
	noteCommenter = 2
	noteEditor    = 3
)

func workspaceRole(s string) int {
	switch s {
	case "guest":
		return roleGuest
	case "member":
		return roleMember
	case "admin":
		return roleAdmin
	case "owner":
		return roleOwner
	}
	return roleNone
}

func noteRole(s string) int {
	switch s {
	case "viewer":
		return noteViewer
	case "commenter":
		return noteCommenter
	case "editor":
		return noteEditor
	}
	return roleNone
}

// Policy is app.Authorizer over a dataset: the permission rules of
// docs/seed-data.md ("Permissions", rules 1 to 7), for user subjects,
// evaluated at the time of the fact being notified about. It stands in for
// identity when replaying history, which identity cannot do (it answers for
// the present). Tests check it against truth/permissions/matrix.jsonl.
type Policy struct {
	notes       map[string]Note
	memberships map[string][]Membership // workspace "/" user
	overrides   map[string]Override     // note "/" user
}

// NewPolicy indexes the dataset's permission data.
func NewPolicy(d *Dataset) *Policy {
	p := &Policy{notes: map[string]Note{}, memberships: map[string][]Membership{}, overrides: map[string]Override{}}
	for _, n := range d.Notes {
		p.notes[n.ID] = n
	}
	for _, m := range d.Memberships {
		k := m.WorkspaceID + "/" + m.UserID
		p.memberships[k] = append(p.memberships[k], m)
	}
	for _, o := range d.Overrides {
		p.overrides[o.NoteID+"/"+o.UserID] = o
	}
	return p
}

// Authorize implements app.Authorizer.
func (p *Policy) Authorize(_ context.Context, userID string, checks []app.Check, at time.Time) ([]bool, error) {
	out := make([]bool, len(checks))
	for i, c := range checks {
		out[i] = p.Allowed(userID, c.Action, c.ResourceID, at)
	}
	return out, nil
}

// workspaceRoleAt is the user's role while a member (joined at or before
// t, not removed at or before t).
func (p *Policy) workspaceRoleAt(workspaceID, userID string, t time.Time) int {
	for _, m := range p.memberships[workspaceID+"/"+userID] {
		if !m.JoinedAt.After(t) && (m.RemovedAt == nil || m.RemovedAt.After(t)) {
			return workspaceRole(m.Role)
		}
	}
	return roleNone
}

// noteRoleAt applies rules 1 to 4: no access without membership; owners and
// admins edit everything; an override in force decides for members and
// guests; members otherwise edit; guests otherwise see nothing.
func (p *Policy) noteRoleAt(n Note, userID string, t time.Time) (note, workspace int) {
	ws := p.workspaceRoleAt(n.WorkspaceID, userID, t)
	switch {
	case ws == roleNone:
		return roleNone, ws
	case ws >= roleAdmin:
		return noteEditor, ws
	}
	if o, ok := p.overrides[n.ID+"/"+userID]; ok && !o.CreatedAt.After(t) {
		return noteRole(o.Role), ws
	}
	if ws == roleMember {
		return noteEditor, ws
	}
	return roleNone, ws
}

// Allowed decides one check at time t. It covers the actions this service
// asks about: NOTE_READ, NOTE_COMMENT, NOTE_EDIT and NOTE_CREATE.
func (p *Policy) Allowed(userID string, action app.Action, resource string, t time.Time) bool {
	if action == app.ActionNoteCreate {
		return p.workspaceRoleAt(resource, userID, t) >= roleMember
	}
	n, ok := p.notes[resource]
	if !ok {
		return false
	}
	role, ws := p.noteRoleAt(n, userID, t)
	if n.TrashedAt != nil && !n.TrashedAt.After(t) {
		// Rule 6: from trashed_at only owners and admins can read.
		return ws >= roleAdmin && action == app.ActionNoteRead
	}
	switch action {
	case app.ActionNoteRead:
		return role >= noteViewer
	case app.ActionNoteComment:
		return role >= noteCommenter
	case app.ActionNoteEdit:
		return role >= noteEditor
	}
	return false
}
