package domain_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	. "github.com/genuinebnt/skein-go/services/identity/internal/domain"
	"github.com/genuinebnt/skein-go/services/identity/internal/seed"
)

var (
	t0  = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	ago = func(d time.Duration) time.Time { return t0.Add(-d) }
)

func ptr(t time.Time) *time.Time { return &t }

// world is a small workspace exercising every rule. Names: o owner, a
// admin, m member (creator of noteM), g guest, r removed member (with a
// stale override), late a member joining after t0, z outsider; mv member
// lowered to viewer, mc member lowered to commenter, mfut member with an
// override created after t0, gv/gc/ge guests granted viewer, commenter and
// editor.
const (
	ws, otherWS                     = WorkspaceID("ws"), WorkspaceID("other-ws")
	o, a, m, g, r, late, z          = UserID("o"), UserID("a"), UserID("m"), UserID("g"), UserID("r"), UserID("late"), UserID("z")
	mv, mc, mfut, gv, gc, ge        = UserID("mv"), UserID("mc"), UserID("mfut"), UserID("gv"), UserID("gc"), UserID("ge")
	noteM, noteO, trashed, trashFut = NoteID("note-m"), NoteID("note-o"), NoteID("trashed"), NoteID("trash-later")
	otherNote, missing              = NoteID("other-note"), NoteID("missing")
)

func world() *Facts {
	f := NewFacts()
	f.AddWorkspace(ws)
	f.AddWorkspace(otherWS)
	f.AddNote(NoteAccess{ID: noteM, WorkspaceID: ws, CreatedBy: m})
	f.AddNote(NoteAccess{ID: noteO, WorkspaceID: ws, CreatedBy: o})
	f.AddNote(NoteAccess{ID: trashed, WorkspaceID: ws, CreatedBy: m, TrashedAt: ptr(ago(time.Hour))})
	f.AddNote(NoteAccess{ID: trashFut, WorkspaceID: ws, CreatedBy: m, TrashedAt: ptr(t0.Add(time.Hour))})
	f.AddNote(NoteAccess{ID: otherNote, WorkspaceID: otherWS, CreatedBy: z})
	member := func(u UserID, role WorkspaceRole, joined time.Time, removed *time.Time) {
		f.AddMembership(Membership{WorkspaceID: ws, UserID: u, Role: role, JoinedAt: joined, RemovedAt: removed})
	}
	member(o, WorkspaceRoleOwner, ago(100*time.Hour), nil)
	member(a, WorkspaceRoleAdmin, ago(90*time.Hour), nil)
	member(m, WorkspaceRoleMember, ago(80*time.Hour), nil)
	member(g, WorkspaceRoleGuest, ago(70*time.Hour), nil)
	member(r, WorkspaceRoleMember, ago(60*time.Hour), ptr(t0)) // removed exactly at t0: rule 1 says "at or before t"
	member(late, WorkspaceRoleMember, t0.Add(time.Second), nil)
	for _, u := range []UserID{mv, mc, mfut} {
		member(u, WorkspaceRoleMember, ago(50*time.Hour), nil)
	}
	for _, u := range []UserID{gv, gc, ge} {
		member(u, WorkspaceRoleGuest, ago(50*time.Hour), nil)
	}
	f.AddMembership(Membership{WorkspaceID: otherWS, UserID: z, Role: WorkspaceRoleOwner, JoinedAt: ago(time.Hour)})
	override := func(u UserID, n NoteID, role NoteRole, at time.Time) {
		f.AddOverride(NoteRoleOverride{NoteID: n, WorkspaceID: ws, UserID: u, Role: role, CreatedAt: at})
	}
	override(mv, noteM, NoteRoleViewer, ago(time.Hour))
	override(mc, noteM, NoteRoleCommenter, ago(time.Hour))
	override(mfut, noteM, NoteRoleViewer, t0.Add(time.Minute))
	override(gv, noteM, NoteRoleViewer, ago(time.Hour))
	override(gc, noteM, NoteRoleCommenter, ago(time.Hour))
	override(ge, noteM, NoteRoleEditor, ago(time.Hour))
	override(ge, trashed, NoteRoleEditor, ago(time.Hour))
	override(r, noteM, NoteRoleEditor, ago(70*time.Hour))
	override(a, noteM, NoteRoleViewer, ago(time.Hour)) // ignored: admins are editors everywhere
	link := func(id string, n NoteID, role NoteRole, created time.Time, expires, revoked *time.Time) {
		f.AddShareLink(ShareLink{ID: ShareLinkID(id), NoteID: n, WorkspaceID: ws, Role: role, CreatedAt: created, ExpiresAt: expires, RevokedAt: revoked})
	}
	link("lv", noteM, NoteRoleViewer, ago(time.Hour), nil, nil)
	link("lc", noteM, NoteRoleCommenter, ago(time.Hour), ptr(t0.Add(time.Hour)), nil)
	link("le", noteM, NoteRoleEditor, ago(time.Hour), nil, nil)
	link("expired", noteM, NoteRoleViewer, ago(time.Hour), ptr(t0), nil) // expires_at is exclusive
	link("revoked", noteM, NoteRoleViewer, ago(time.Hour), nil, ptr(t0))
	link("future", noteM, NoteRoleViewer, t0.Add(time.Second), nil, nil)
	link("on-trash", trashed, NoteRoleViewer, ago(2*time.Hour), nil, nil)
	return f
}

func user(u UserID) Subject              { return UserSubject(u, "") }
func agent(u UserID) Subject             { return UserSubject(u, AgentRunID("run-"+string(u))) }
func link(id string) Subject             { return LinkSubject(ShareLinkID(id)) }
func onNote(a Action, n NoteID) Check    { return Check{Action: a, ResourceID: string(n)} }
func onWS(a Action, w WorkspaceID) Check { return Check{Action: a, ResourceID: string(w)} }

func TestDecideFollowsEveryRule(t *testing.T) {
	f := world()
	cases := []struct {
		name    string
		subj    Subject
		check   Check
		allowed bool
		rule    string
	}{
		// Rule 1: membership.
		{"outsider cannot read", user(z), onNote(ActionNoteRead, noteM), false, "not_member"},
		{"member joining after t cannot read yet", user(late), onNote(ActionNoteRead, noteM), false, "not_member"},
		{"member removed at t loses access at t", user(r), onNote(ActionNoteRead, noteM), false, "membership_removed"},
		{"removed member's editor override is void", user(r), onNote(ActionNoteEdit, noteM), false, "membership_removed"},
		{"removed member cannot create notes", user(r), onWS(ActionNoteCreate, ws), false, "membership_removed"},
		{"outsider cannot create notes", user(z), onWS(ActionNoteCreate, ws), false, "not_member"},
		// Rule 2: owners and admins.
		{"owner edits any note", user(o), onNote(ActionNoteEdit, noteM), true, "workspace_role:owner"},
		{"admin ignores a viewer override", user(a), onNote(ActionNoteEdit, noteM), true, "workspace_role:admin"},
		{"admin deletes a note they did not create", user(a), onNote(ActionNoteDelete, noteM), true, "workspace_role:admin"},
		{"owner shares", user(o), onNote(ActionNoteShare, noteM), true, "workspace_role:owner"},
		// Rule 3: members.
		{"member edits by default", user(m), onNote(ActionNoteEdit, noteO), true, "workspace_role:member"},
		{"member lowered to viewer reads", user(mv), onNote(ActionNoteRead, noteM), true, "note_override:viewer"},
		{"member lowered to viewer cannot comment", user(mv), onNote(ActionNoteComment, noteM), false, "note_override:viewer"},
		{"member lowered to commenter comments", user(mc), onNote(ActionNoteComment, noteM), true, "note_override:commenter"},
		{"member lowered to commenter cannot edit", user(mc), onNote(ActionNoteEdit, noteM), false, "note_override:commenter"},
		{"override only on its own note", user(mv), onNote(ActionNoteEdit, noteO), true, "workspace_role:member"},
		{"override created after t does not apply yet", user(mfut), onNote(ActionNoteEdit, noteM), true, "workspace_role:member"},
		{"lowered member cannot share", user(mv), onNote(ActionNoteShare, noteM), false, "note_override:viewer"},
		// Rule 4: guests.
		{"guest without grant cannot read", user(g), onNote(ActionNoteRead, noteM), false, "guest_without_grant"},
		{"guest granted viewer reads", user(gv), onNote(ActionNoteRead, noteM), true, "note_override:viewer"},
		{"guest granted commenter comments", user(gc), onNote(ActionNoteComment, noteM), true, "note_override:commenter"},
		{"guest granted editor edits", user(ge), onNote(ActionNoteEdit, noteM), true, "note_override:editor"},
		{"guest editor cannot share", user(ge), onNote(ActionNoteShare, noteM), false, "guest_cannot_share"},
		{"guest editor cannot delete another's note", user(ge), onNote(ActionNoteDelete, noteM), false, "delete_needs_creator_or_admin"},
		{"guest grant only on its own note", user(gv), onNote(ActionNoteRead, noteO), false, "guest_without_grant"},
		// Rule 5: share links.
		{"viewer link reads", link("lv"), onNote(ActionNoteRead, noteM), true, "share_link:viewer"},
		{"viewer link cannot comment", link("lv"), onNote(ActionNoteComment, noteM), false, "share_link:viewer"},
		{"commenter link comments before expiry", link("lc"), onNote(ActionNoteComment, noteM), true, "share_link:commenter"},
		{"editor link is clamped to commenter", link("le"), onNote(ActionNoteEdit, noteM), false, "share_link:commenter"},
		{"clamped editor link still comments", link("le"), onNote(ActionNoteComment, noteM), true, "share_link:commenter"},
		{"link expiring at t is expired at t", link("expired"), onNote(ActionNoteRead, noteM), false, "share_link:expired"},
		{"link revoked at t is revoked at t", link("revoked"), onNote(ActionNoteRead, noteM), false, "share_link:revoked"},
		{"link created after t does not work yet", link("future"), onNote(ActionNoteRead, noteM), false, "share_link:not_yet_created"},
		{"link only works on its own note", link("lv"), onNote(ActionNoteRead, noteO), false, "share_link:other_note"},
		{"unknown link", link("nope"), onNote(ActionNoteRead, noteM), false, "share_link:unknown"},
		{"link cannot share", link("lv"), onNote(ActionNoteShare, noteM), false, "share_link:cannot_manage"},
		{"link cannot delete", link("lv"), onNote(ActionNoteDelete, noteM), false, "share_link:cannot_manage"},
		{"link cannot manage a workspace", link("lv"), onWS(ActionNoteCreate, ws), false, "share_link:cannot_manage"},
		{"link to a trashed note is refused", link("on-trash"), onNote(ActionNoteRead, trashed), false, "note:trashed"},
		// Rule 6: trash.
		{"owner reads a trashed note", user(o), onNote(ActionNoteRead, trashed), true, "trashed:owner"},
		{"admin deletes a trashed note", user(a), onNote(ActionNoteDelete, trashed), true, "trashed:admin"},
		{"owner cannot edit a trashed note", user(o), onNote(ActionNoteEdit, trashed), false, "note:trashed"},
		{"creator cannot read their trashed note", user(m), onNote(ActionNoteRead, trashed), false, "note:trashed"},
		{"guest editor cannot read a trashed note", user(ge), onNote(ActionNoteRead, trashed), false, "note:trashed"},
		{"outsider asking about a trashed note", user(z), onNote(ActionNoteRead, trashed), false, "note:trashed"},
		{"note trashed after t is still readable", user(m), onNote(ActionNoteRead, trashFut), true, "workspace_role:member"},
		// Rule 7: deletion and sharing.
		{"creator deletes their own note", user(m), onNote(ActionNoteDelete, noteM), true, "creator"},
		{"member cannot delete another's note", user(m), onNote(ActionNoteDelete, noteO), false, "delete_needs_creator_or_admin"},
		{"viewer cannot delete", user(mv), onNote(ActionNoteDelete, noteM), false, "note_override:viewer"},
		{"member shares", user(m), onNote(ActionNoteShare, noteO), true, "workspace_role:member"},
		// Workspace actions.
		{"member creates notes", user(m), onWS(ActionNoteCreate, ws), true, "workspace_role:member"},
		{"guest cannot create notes", user(g), onWS(ActionNoteCreate, ws), false, "workspace_role:guest"},
		{"guest cannot start agent runs", user(g), onWS(ActionAgentRun, ws), false, "workspace_role:guest"},
		{"member starts agent runs", user(m), onWS(ActionAgentRun, ws), true, "workspace_role:member"},
		{"member cannot manage members", user(m), onWS(ActionManageMembers, ws), false, "workspace_role:member"},
		{"admin manages members", user(a), onWS(ActionManageMembers, ws), true, "workspace_role:admin"},
		{"admin cannot manage settings", user(a), onWS(ActionManageSettings, ws), false, "workspace_role:admin"},
		{"owner manages settings", user(o), onWS(ActionManageSettings, ws), true, "workspace_role:owner"},
		// Agent runs act as their user.
		{"agent run of a viewer cannot edit", agent(mv), onNote(ActionNoteEdit, noteM), false, "note_override:viewer"},
		{"agent run of a member edits", agent(m), onNote(ActionNoteEdit, noteO), true, "workspace_role:member"},
		// Unknown things.
		{"unknown note", user(o), onNote(ActionNoteRead, missing), false, "note:unknown"},
		{"unknown workspace", user(o), onWS(ActionNoteCreate, "nope"), false, "workspace:unknown"},
		{"unspecified action", user(o), Check{Action: ActionUnspecified, ResourceID: string(noteM)}, false, "action:unknown"},
		{"action out of range", user(o), Check{Action: 99, ResourceID: string(noteM)}, false, "action:unknown"},
		{"note action on a workspace ID", user(o), onNote(ActionNoteRead, NoteID(ws)), false, "note:unknown"},
		{"zero subject", Subject{}, onNote(ActionNoteRead, noteM), false, "subject:invalid"},
		{"other workspace's owner has no say here", user(z), onNote(ActionNoteDelete, noteM), false, "not_member"},
		{"other workspace's note for this owner", user(o), onNote(ActionNoteRead, otherNote), false, "not_member"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(f, c.subj, c.check, t0)
			if got.Allowed != c.allowed || got.Rule != c.rule {
				t.Fatalf("Decide = %+v, want allowed=%v rule=%q", got, c.allowed, c.rule)
			}
		})
	}
}

// everySubject and everyResource span the world for exhaustive checks.
func everySubject() []Subject {
	var out []Subject
	for _, u := range []UserID{o, a, m, g, r, late, z, mv, mc, mfut, gv, gc, ge, "nobody"} {
		out = append(out, user(u), agent(u))
	}
	for _, l := range []string{"lv", "lc", "le", "expired", "revoked", "future", "on-trash", "nope"} {
		out = append(out, link(l))
	}
	return out
}

func everyCheck() []Check {
	var out []Check
	for _, act := range AllActions {
		if act.Resource() == ResourceNote {
			for _, n := range []NoteID{noteM, noteO, trashed, trashFut, otherNote, missing} {
				out = append(out, onNote(act, n))
			}
		} else {
			for _, w := range []WorkspaceID{ws, otherWS, "nope"} {
				out = append(out, onWS(act, w))
			}
		}
	}
	return out
}

// TestDecisionsAreConsistentAcrossEverySubjectActionAndResource checks
// properties that must hold for every combination, so a rule edited in
// one place cannot contradict another.
func TestDecisionsAreConsistentAcrossEverySubjectActionAndResource(t *testing.T) {
	f := world()
	allowed := func(s Subject, a Action, res string) bool {
		return Decide(f, s, Check{Action: a, ResourceID: res}, t0).Allowed
	}
	for _, s := range everySubject() {
		for _, c := range everyCheck() {
			d := Decide(f, s, c, t0)
			if d.Rule == "" {
				t.Fatalf("%+v %v: decision without a rule", s, c)
			}
			res := c.ResourceID
			if s.Kind == SubjectAgentRun {
				if u := Decide(f, user(s.UserID), c, t0); u != d {
					t.Errorf("agent %s %v: %+v, user decides %+v", s.UserID, c, d, u)
				}
			}
			if !d.Allowed {
				continue
			}
			switch c.Action {
			case ActionNoteEdit:
				if !allowed(s, ActionNoteComment, res) {
					t.Errorf("%+v may edit %s but not comment", s, res)
				}
				if s.Kind == SubjectShareLink {
					t.Errorf("share link %s may edit", s.ShareLinkID)
				}
			case ActionNoteComment:
				if !allowed(s, ActionNoteRead, res) {
					t.Errorf("%+v may comment on %s but not read", s, res)
				}
			case ActionNoteShare:
				if !allowed(s, ActionNoteEdit, res) {
					t.Errorf("%+v may share %s but not edit", s, res)
				}
			case ActionNoteDelete:
				if !allowed(s, ActionNoteRead, res) {
					t.Errorf("%+v may delete %s but not read", s, res)
				}
			case ActionManageSettings:
				if !allowed(s, ActionManageMembers, res) {
					t.Errorf("%+v may manage settings but not members", s)
				}
			case ActionManageMembers:
				if !allowed(s, ActionNoteCreate, res) {
					t.Errorf("%+v may manage members but not create notes", s)
				}
			}
			if n, ok := f.Note(NoteID(res)); ok && n.TrashedBy(t0) {
				role, _ := f.WorkspaceRoleAt(n.WorkspaceID, s.UserID, t0)
				if role < WorkspaceRoleAdmin || (c.Action != ActionNoteRead && c.Action != ActionNoteDelete) {
					t.Errorf("%+v may %v trashed note %s", s, c.Action, res)
				}
			}
		}
	}
}

func TestAuthorizeAnswersInRequestOrder(t *testing.T) {
	f := world()
	checks := []Check{onNote(ActionNoteEdit, noteM), onNote(ActionNoteRead, missing), onWS(ActionManageSettings, ws), onNote(ActionNoteRead, noteM)}
	got := Authorize(f, user(mv), checks, t0)
	want := []bool{false, false, false, true}
	for i := range want {
		if got[i].Allowed != want[i] {
			t.Fatalf("check %d: %+v, want %v", i, got[i], want[i])
		}
	}
}

func TestReadableNotesIsExactlyTheNotesDecideLetsYouRead(t *testing.T) {
	f := world()
	all := []NoteAccess{}
	for _, id := range []NoteID{trashFut, noteO, noteM, trashed, otherNote, noteM} {
		n, _ := f.Note(id)
		all = append(all, n)
	}
	for _, s := range everySubject() {
		got := ReadableNotes(f, s, all, t0)
		var want []NoteID
		for _, id := range []NoteID{noteM, noteO, otherNote, trashFut, trashed} {
			if Decide(f, s, onNote(ActionNoteRead, id), t0).Allowed {
				want = append(want, id)
			}
		}
		if fmt.Sprint(sortedIDs(want)) != fmt.Sprint(got) {
			t.Errorf("%+v: ReadableNotes = %v, want %v", s, got, sortedIDs(want))
		}
	}
	if got := ReadableNotes(f, user(o), all, t0); fmt.Sprint(got) != "[note-m note-o trash-later trashed]" {
		t.Errorf("owner reads %v; owners must see trashed notes", got)
	}
	if got := ReadableNotes(f, user(m), all, t0); fmt.Sprint(got) != "[note-m note-o trash-later]" {
		t.Errorf("member reads %v; members must not see trashed notes", got)
	}
}

func sortedIDs(ids []NoteID) []NoteID {
	out := append([]NoteID(nil), ids...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Seed matrix: every row of truth/permissions/matrix.jsonl, decided from
// Facts built straight from the seed files.

var smallSeed = filepath.Join("..", "..", "..", "..", "testdata", "seed", "small")

type matrixRow struct {
	WorkspaceID string `json:"workspace_id"`
	Subject     struct {
		UserID      *string `json:"user_id"`
		AgentRunID  *string `json:"agent_run_id"`
		ShareLinkID *string `json:"share_link_id"`
	} `json:"subject"`
	Action     string    `json:"action"`
	ResourceID string    `json:"resource_id"`
	At         time.Time `json:"at"`
	Expected   string    `json:"expected"`
	Rule       string    `json:"rule"`
}

func (r matrixRow) subject() Subject {
	switch {
	case r.Subject.ShareLinkID != nil:
		return LinkSubject(ShareLinkID(*r.Subject.ShareLinkID))
	case r.Subject.AgentRunID != nil:
		return UserSubject(UserID(*r.Subject.UserID), AgentRunID(*r.Subject.AgentRunID))
	default:
		return UserSubject(UserID(*r.Subject.UserID), "")
	}
}

func seedFacts(t *testing.T) *Facts {
	t.Helper()
	notes, err := seed.ReadJSONL[struct {
		ID          string     `json:"id"`
		WorkspaceID string     `json:"workspace_id"`
		CreatedBy   string     `json:"created_by"`
		TrashedAt   *time.Time `json:"trashed_at"`
	}](smallSeed, "notes.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	f := NewFacts()
	ws := map[string]string{}
	for _, n := range notes {
		f.AddNote(NoteAccess{ID: NoteID(n.ID), WorkspaceID: WorkspaceID(n.WorkspaceID), CreatedBy: UserID(n.CreatedBy), TrashedAt: n.TrashedAt})
		ws[n.ID] = n.WorkspaceID
	}
	workspaces, err := seed.ReadJSONL[struct {
		ID string `json:"id"`
	}](smallSeed, "workspaces.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range workspaces {
		f.AddWorkspace(WorkspaceID(w.ID))
	}
	ms, err := seed.ReadJSONL[struct {
		WorkspaceID string     `json:"workspace_id"`
		UserID      string     `json:"user_id"`
		Role        string     `json:"role"`
		JoinedAt    time.Time  `json:"joined_at"`
		RemovedAt   *time.Time `json:"removed_at"`
	}](smallSeed, "memberships.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, mm := range ms {
		f.AddMembership(Membership{WorkspaceID: WorkspaceID(mm.WorkspaceID), UserID: UserID(mm.UserID),
			Role: ParseWorkspaceRole(mm.Role), JoinedAt: mm.JoinedAt, RemovedAt: mm.RemovedAt})
	}
	acl, err := seed.ReadJSONL[struct {
		NoteID    string    `json:"note_id"`
		UserID    string    `json:"user_id"`
		Role      string    `json:"role"`
		CreatedAt time.Time `json:"created_at"`
	}](smallSeed, "acl.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range acl {
		f.AddOverride(NoteRoleOverride{NoteID: NoteID(o.NoteID), WorkspaceID: WorkspaceID(ws[o.NoteID]),
			UserID: UserID(o.UserID), Role: ParseNoteRole(o.Role), CreatedAt: o.CreatedAt})
	}
	links, err := seed.ReadJSONL[struct {
		ID        string     `json:"id"`
		NoteID    string     `json:"note_id"`
		Role      string     `json:"role"`
		CreatedAt time.Time  `json:"created_at"`
		ExpiresAt *time.Time `json:"expires_at"`
		RevokedAt *time.Time `json:"revoked_at"`
	}](smallSeed, "share_links.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		f.AddShareLink(ShareLink{ID: ShareLinkID(l.ID), NoteID: NoteID(l.NoteID), Role: ParseNoteRole(l.Role),
			CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt, RevokedAt: l.RevokedAt})
	}
	return f
}

func TestDecideMatchesEverySeedPermissionMatrixRow(t *testing.T) {
	rows, err := seed.ReadJSONL[matrixRow](smallSeed, filepath.Join("truth", "permissions", "matrix.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6256 {
		t.Fatalf("matrix has %d rows, the small seed has 6256", len(rows))
	}
	f := seedFacts(t)
	mismatches := 0
	for i, row := range rows {
		act := ParseAction(row.Action)
		if act == ActionUnspecified {
			t.Fatalf("row %d: unknown action %q", i, row.Action)
		}
		d := Decide(f, row.subject(), Check{Action: act, ResourceID: row.ResourceID}, row.At)
		if d.Allowed != (row.Expected == "allow") || d.Rule != row.Rule {
			mismatches++
			if mismatches <= 20 {
				t.Errorf("row %d (%s %s on %s): got allowed=%v rule=%q, want %s rule=%q",
					i, row.subject().UserID+UserID(row.subject().ShareLinkID), row.Action, row.ResourceID, d.Allowed, d.Rule, row.Expected, row.Rule)
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d of %d matrix rows disagree", mismatches, len(rows))
	}
}
