package policy

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

func at(days int) time.Time { return t0.Add(time.Duration(days) * 24 * time.Hour) }

func fixture() *State {
	s := NewState()
	s.AddWorkspace("w")
	removed := at(10)
	for _, m := range []Membership{
		{WorkspaceID: "w", UserID: "owner", Role: Owner, JoinedAt: at(0)},
		{WorkspaceID: "w", UserID: "member", Role: Member, JoinedAt: at(0)},
		{WorkspaceID: "w", UserID: "lowered", Role: Member, JoinedAt: at(0)},
		{WorkspaceID: "w", UserID: "guest", Role: Guest, JoinedAt: at(0)},
		{WorkspaceID: "w", UserID: "leaver", Role: Member, JoinedAt: at(0), RemovedAt: &removed},
	} {
		s.AddMembership(m)
	}
	trashed := at(20)
	s.AddNote(Note{ID: "n", WorkspaceID: "w", CreatedBy: "member"})
	s.AddNote(Note{ID: "t", WorkspaceID: "w", CreatedBy: "member", TrashedAt: &trashed})
	s.AddOverride(Override{NoteID: "n", UserID: "lowered", Role: Viewer, CreatedAt: at(1)})
	s.AddOverride(Override{NoteID: "n", UserID: "guest", Role: Commenter, CreatedAt: at(1)})
	s.AddOverride(Override{NoteID: "n", UserID: "leaver", Role: Viewer, CreatedAt: at(1)})
	exp := at(5)
	rev := at(3)
	s.AddShareLink(ShareLink{ID: "live", NoteID: "n", Role: Viewer, CreatedAt: at(1)})
	s.AddShareLink(ShareLink{ID: "expiring", NoteID: "n", Role: Editor, CreatedAt: at(1), ExpiresAt: &exp})
	s.AddShareLink(ShareLink{ID: "revoked", NoteID: "n", Role: Editor, CreatedAt: at(1), RevokedAt: &rev})
	return s
}

func TestPolicyRules(t *testing.T) {
	s := fixture()
	u := func(id string) Subject { return Subject{UserID: id} }
	cases := []struct {
		name    string
		subj    Subject
		act     Action
		res     string
		when    time.Time
		allowed bool
		rule    string
	}{
		{"member edits by default", u("member"), NoteEdit, "n", at(2), true, "workspace_role:member"},
		{"override lowers a member to viewer", u("lowered"), NoteEdit, "n", at(2), false, "note_override:viewer"},
		{"lowered member can still read", u("lowered"), NoteRead, "n", at(2), true, "note_override:viewer"},
		{"override applies only from its creation", u("lowered"), NoteEdit, "n", at(0), true, "workspace_role:member"},
		{"guest comments with a commenter grant", u("guest"), NoteComment, "n", at(2), true, "note_override:commenter"},
		{"guest without grant sees nothing", u("guest"), NoteRead, "t", at(2), false, "guest_without_grant"},
		{"guest cannot share even with a grant", u("guest"), NoteShare, "n", at(2), false, "note_override:commenter"},
		{"removed member loses access and overrides", u("leaver"), NoteRead, "n", at(11), false, "membership_removed"},
		{"removed member had access before", u("leaver"), NoteRead, "n", at(9), true, "note_override:viewer"},
		{"outsider", u("stranger"), NoteRead, "n", at(2), false, "not_member"},
		{"member deletes own note", u("member"), NoteDelete, "n", at(2), true, "creator"},
		{"trashed note hidden from its creator", u("member"), NoteRead, "t", at(21), false, "note:trashed"},
		{"owner reads the trash", u("owner"), NoteRead, "t", at(21), true, "trashed:owner"},
		{"trash is time dependent", u("member"), NoteEdit, "t", at(19), true, "workspace_role:member"},
		{"live link reads", Subject{ShareLinkID: "live"}, NoteRead, "n", at(30), true, "share_link:viewer"},
		{"viewer link cannot edit", Subject{ShareLinkID: "live"}, NoteEdit, "n", at(30), false, "share_link:viewer"},
		{"link expires", Subject{ShareLinkID: "expiring"}, NoteEdit, "n", at(5), false, "share_link:expired"},
		{"link works before expiry", Subject{ShareLinkID: "expiring"}, NoteEdit, "n", at(4), true, "share_link:editor"},
		{"revoked link", Subject{ShareLinkID: "revoked"}, NoteRead, "n", at(3), false, "share_link:revoked"},
		{"link cannot manage", Subject{ShareLinkID: "live"}, NoteDelete, "n", at(2), false, "share_link:cannot_manage"},
		{"agent run acts as its user", Subject{UserID: "lowered", AgentRunID: "run"}, NoteEdit, "n", at(2), false, "note_override:viewer"},
		{"guests cannot run the agent", u("guest"), AgentRun, "w", at(2), false, "workspace_role:guest"},
		{"only the owner manages settings", u("member"), ManageSettings, "w", at(2), false, "workspace_role:member"},
	}
	for _, c := range cases {
		d := s.Evaluate(c.subj, c.act, c.res, c.when)
		if d.Allowed != c.allowed || d.Rule != c.rule {
			t.Errorf("%s: got %v (%s), want %v (%s)", c.name, d.Allowed, d.Rule, c.allowed, c.rule)
		}
	}
}
