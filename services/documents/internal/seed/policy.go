package seed

import (
	"context"
	"slices"
	"time"

	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/ports"
)

// Policy is a ports.Authorizer that decides with the seed's documented
// policy (docs/seed-data.md, "Permissions") at the time its clock shows.
// It covers user subjects and the actions documents checks; share links
// and workspace management are identity's business and not needed here.
type Policy struct {
	clock       ports.Clock
	memberships map[domain.WorkspaceID][]membership
	overrides   map[domain.NoteID][]override
	notes       map[domain.NoteID]policyNote
}

type membership struct {
	user            string
	role            string
	joined, removed time.Time
}

type override struct {
	user    string
	role    string
	created time.Time
}

type policyNote struct {
	workspace domain.WorkspaceID
	creator   string
	trashed   time.Time
}

const (
	roleNone = iota
	roleViewer
	roleCommenter
	roleEditor
)

var noteRoles = map[string]int{"viewer": roleViewer, "commenter": roleCommenter, "editor": roleEditor}

// NewPolicy reads memberships, overrides and notes from the dataset.
func NewPolicy(d *Dataset, clock ports.Clock) (*Policy, error) {
	ms, err := readJSONL[membershipRecord](d, "memberships.jsonl")
	if err != nil {
		return nil, err
	}
	acl, err := readJSONL[aclRecord](d, "acl.jsonl")
	if err != nil {
		return nil, err
	}
	notes, err := readJSONL[noteRecord](d, "notes.jsonl")
	if err != nil {
		return nil, err
	}
	p := &Policy{clock: clock, memberships: map[domain.WorkspaceID][]membership{}, overrides: map[domain.NoteID][]override{}, notes: map[domain.NoteID]policyNote{}}
	for _, m := range ms {
		joined, err := parseTime(m.JoinedAt)
		if err != nil {
			return nil, err
		}
		removed, err := parseOptionalTime(m.RemovedAt)
		if err != nil {
			return nil, err
		}
		ws := domain.WorkspaceID(m.WorkspaceID)
		p.memberships[ws] = append(p.memberships[ws], membership{m.UserID, m.Role, joined, removed})
	}
	for _, a := range acl {
		created, err := parseTime(a.CreatedAt)
		if err != nil {
			return nil, err
		}
		id := domain.NoteID(a.NoteID)
		p.overrides[id] = append(p.overrides[id], override{a.UserID, a.Role, created})
	}
	for _, n := range notes {
		trashed, err := parseOptionalTime(n.TrashedAt)
		if err != nil {
			return nil, err
		}
		p.notes[domain.NoteID(n.ID)] = policyNote{domain.WorkspaceID(n.WorkspaceID), n.CreatedBy, trashed}
	}
	return p, nil
}

// role returns the user's workspace role at t: "" if not a member then
// (never joined, or removed at or before t; rule 1).
func (p *Policy) role(ws domain.WorkspaceID, user string, t time.Time) string {
	for _, m := range p.memberships[ws] {
		if m.user == user && !m.joined.After(t) && (m.removed.IsZero() || m.removed.After(t)) {
			return m.role
		}
	}
	return ""
}

// noteRole is the user's role on a note at t (rules 2 to 4): owners and
// admins edit everything; members edit unless the latest override made at
// or before t says otherwise; guests get only what an override grants.
func (p *Policy) noteRole(id domain.NoteID, n policyNote, wsRole, user string, t time.Time) int {
	if wsRole == "owner" || wsRole == "admin" {
		return roleEditor
	}
	role := roleNone
	if wsRole == "member" {
		role = roleEditor
	}
	var latest time.Time
	for _, o := range p.overrides[id] {
		if o.user == user && !o.created.After(t) && !o.created.Before(latest) {
			role, latest = noteRoles[o.role], o.created
		}
	}
	return role
}

func (p *Policy) allowed(subject domain.Subject, c ports.Check, t time.Time) bool {
	if c.Action == ports.ActionCreate {
		r := p.role(domain.WorkspaceID(c.Resource), subject.UserID, t)
		return r == "owner" || r == "admin" || r == "member"
	}
	id := domain.NoteID(c.Resource)
	n, ok := p.notes[id]
	if !ok {
		return false
	}
	wsRole := p.role(n.workspace, subject.UserID, t)
	if wsRole == "" {
		return false
	}
	admin := wsRole == "owner" || wsRole == "admin"
	trashed := !n.trashed.IsZero() && !n.trashed.After(t)
	role := p.noteRole(id, n, wsRole, subject.UserID, t)
	switch c.Action {
	case ports.ActionRead:
		return role >= roleViewer && (!trashed || admin) // rule 6
	case ports.ActionEdit:
		return role >= roleEditor && !trashed
	case ports.ActionDelete:
		return admin || (!trashed && n.creator == subject.UserID && role >= roleEditor) // rule 7
	}
	return false
}

// Authorize implements ports.Authorizer. An agent run is decided as its
// user (ADR-0009).
func (p *Policy) Authorize(_ context.Context, subject domain.Subject, checks []ports.Check) ([]bool, error) {
	t := p.clock.Now()
	out := make([]bool, len(checks))
	for i, c := range checks {
		out[i] = p.allowed(subject, c, t)
	}
	return out, nil
}

// ReadableNotes implements ports.Authorizer.
func (p *Policy) ReadableNotes(_ context.Context, subject domain.Subject, ws domain.WorkspaceID) ([]domain.NoteID, error) {
	t := p.clock.Now()
	var out []domain.NoteID
	for id, n := range p.notes {
		if n.workspace == ws && p.allowed(subject, ports.Check{Action: ports.ActionRead, Resource: string(id)}, t) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}
