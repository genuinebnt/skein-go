package app_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// team is a workspace with an owner, an admin, a member and a guest.
type team struct {
	h                           *harness
	ws                          domain.WorkspaceID
	owner, admin, member, guest domain.User
	outsider                    domain.User
}

func newTeam(t *testing.T, opts ...option) *team {
	h := newHarness(t, opts...)
	tm := &team{h: h}
	tm.owner, _ = h.signUp("owner@example.com")
	tm.admin, _ = h.signUp("admin@example.com")
	tm.member, _ = h.signUp("member@example.com")
	tm.guest, _ = h.signUp("guest@example.com")
	tm.outsider, _ = h.signUp("outsider@example.com")
	tm.ws = h.workspace(tm.owner)
	h.setRole(tm.owner, tm.ws, tm.admin, domain.WorkspaceRoleAdmin)
	h.setRole(tm.admin, tm.ws, tm.member, domain.WorkspaceRoleMember)
	h.setRole(tm.admin, tm.ws, tm.guest, domain.WorkspaceRoleGuest)
	h.store.ResetPublished()
	return tm
}

func TestCreatingAWorkspaceMakesTheCallerItsOwnerAndPublishesIt(t *testing.T) {
	h := newHarness(t)
	u, _ := h.signUp("ada@example.com")
	ws := h.workspace(u)
	page, err := h.svc.ListMembers(ctx, subj(u), ws, 0, "")
	if err != nil || len(page.Members) != 1 || page.Members[0].Role != domain.WorkspaceRoleOwner {
		t.Fatalf("%+v %v", page, err)
	}
	if ev := h.store.Published(); len(ev) != 1 || ev[0].WorkspaceID != ws || ev[0].UserID != u.ID || ev[0].Revoked {
		t.Fatalf("events %+v", ev)
	}
	_, err = h.svc.CreateWorkspace(ctx, subj(u), "")
	if fieldOf(err) != "name" {
		t.Fatalf("blank name: %v", err)
	}
}

func TestAFailedCommitPublishesNothing(t *testing.T) {
	h := newHarness(t)
	u, _ := h.signUp("ada@example.com")
	h.store.ResetPublished()
	h.store.FailNextCommit = errors.New("disk full")
	if _, err := h.svc.CreateWorkspace(ctx, subj(u), "Research"); err == nil {
		t.Fatal("expected the commit failure")
	}
	if ev := h.store.Published(); len(ev) != 0 {
		t.Fatalf("a rolled-back transaction published %+v", ev)
	}
}

func TestListMembersPagesThroughEveryActiveMemberOnce(t *testing.T) {
	tm := newTeam(t)
	h := tm.h
	for i := range 6 {
		u, _ := h.signUp(fmt.Sprintf("extra%d@example.com", i))
		h.setRole(tm.owner, tm.ws, u, domain.WorkspaceRoleMember)
	}
	if err := h.svc.RemoveMember(ctx, subj(tm.owner), tm.ws, tm.guest.ID); err != nil {
		t.Fatal(err)
	}
	var all []domain.UserID
	token := ""
	for pages := 0; ; pages++ {
		page, err := h.svc.ListMembers(ctx, subj(tm.member), tm.ws, 3, token)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Members) > 3 {
			t.Fatalf("page of %d", len(page.Members))
		}
		for _, m := range page.Members {
			all = append(all, m.User.ID)
		}
		if token = page.NextPageToken; token == "" {
			break
		}
		if pages > 5 {
			t.Fatal("paging does not end")
		}
	}
	if len(all) != 9 || !slices.IsSorted(all) || slices.Contains(all, tm.guest.ID) {
		t.Fatalf("listed %d members %v", len(all), all)
	}
	other := h.workspace(tm.owner)
	first, _ := h.svc.ListMembers(ctx, subj(tm.owner), tm.ws, 1, "")
	_, err := h.svc.ListMembers(ctx, subj(tm.owner), other, 1, first.NextPageToken)
	if fieldOf(err) != "page.page_token" {
		t.Fatalf("token from another workspace: %v", err)
	}
	_, err = h.svc.ListMembers(ctx, subj(tm.outsider), tm.ws, 0, "")
	wantErr(t, err, domain.ErrNotFound)
}

func TestWorkspaceRoleChangesKeepAnOwnerAndReserveOwnershipToOwners(t *testing.T) {
	tm := newTeam(t)
	h := tm.h
	err := h.svc.SetWorkspaceRole(ctx, subj(tm.admin), tm.ws, tm.member.ID, domain.WorkspaceRoleOwner)
	wantErr(t, err, domain.ErrPermissionDenied)
	err = h.svc.SetWorkspaceRole(ctx, subj(tm.admin), tm.ws, tm.owner.ID, domain.WorkspaceRoleMember)
	wantErr(t, err, domain.ErrPermissionDenied)
	err = h.svc.SetWorkspaceRole(ctx, subj(tm.member), tm.ws, tm.guest.ID, domain.WorkspaceRoleMember)
	wantErr(t, err, domain.ErrPermissionDenied)
	err = h.svc.SetWorkspaceRole(ctx, subj(tm.outsider), tm.ws, tm.guest.ID, domain.WorkspaceRoleMember)
	wantErr(t, err, domain.ErrNotFound)
	err = h.svc.SetWorkspaceRole(ctx, subj(tm.owner), tm.ws, tm.owner.ID, domain.WorkspaceRoleAdmin)
	wantErr(t, err, domain.ErrFailedPrecondition)
	err = h.svc.RemoveMember(ctx, subj(tm.owner), tm.ws, tm.owner.ID)
	wantErr(t, err, domain.ErrFailedPrecondition)
	err = h.svc.SetWorkspaceRole(ctx, subj(tm.owner), tm.ws, tm.member.ID, domain.WorkspaceRoleNone)
	if fieldOf(err) != "role" {
		t.Fatalf("unspecified role: %v", err)
	}
	err = h.svc.SetWorkspaceRole(ctx, subj(tm.owner), tm.ws, "019b7892-1c00-75eb-b4c7-000000000000", domain.WorkspaceRoleMember)
	wantErr(t, err, domain.ErrNotFound)
	if ev := h.store.Published(); len(ev) != 0 {
		t.Fatalf("refused changes published %+v", ev)
	}

	// With a second owner, the first may step down.
	h.setRole(tm.owner, tm.ws, tm.admin, domain.WorkspaceRoleOwner)
	h.setRole(tm.admin, tm.ws, tm.owner, domain.WorkspaceRoleMember)
	ev := h.store.Published()
	if len(ev) != 2 || ev[0].Revoked || !ev[1].Revoked || ev[1].UserID != tm.owner.ID {
		t.Fatalf("events %+v; promotion is not a revocation, demotion is", ev)
	}
	// Setting the same role again changes nothing and publishes nothing.
	h.store.ResetPublished()
	h.setRole(tm.admin, tm.ws, tm.owner, domain.WorkspaceRoleMember)
	if ev := h.store.Published(); len(ev) != 0 {
		t.Fatalf("no-op published %+v", ev)
	}
}

func TestRemovingAMemberRevokesAccessAtOnceDespiteTheCache(t *testing.T) {
	tm := newTeam(t)
	h := tm.h
	note := h.note(tm.ws, tm.owner)
	if err := h.svc.SetNoteRole(ctx, subj(tm.owner), note, tm.member.ID, domain.NoteRoleCommenter); err != nil {
		t.Fatal(err)
	}
	if !h.can(subj(tm.member), domain.ActionNoteComment, string(note)) {
		t.Fatal("member cannot comment before removal")
	}
	d, _ := h.svc.Authorize(ctx, subj(tm.member), []domain.Check{{Action: domain.ActionNoteComment, ResourceID: string(note)}})
	if d[0].Rule != "cache" {
		t.Fatalf("second identical check was not cached: %+v", d[0])
	}
	h.store.ResetPublished()
	if err := h.svc.RemoveMember(ctx, subj(tm.admin), tm.ws, tm.member.ID); err != nil {
		t.Fatal(err)
	}
	h.check()
	if h.can(subj(tm.member), domain.ActionNoteComment, string(note)) {
		t.Fatal("removed member still allowed: the cache was not invalidated")
	}
	if ev := h.store.Published(); len(ev) != 1 || !ev[0].Revoked || ev[0].UserID != tm.member.ID || ev[0].NoteID != "" {
		t.Fatalf("events %+v", ev)
	}
	// Invited back, the old commenter override is gone: editor again.
	h.setRole(tm.admin, tm.ws, tm.member, domain.WorkspaceRoleMember)
	if !h.can(subj(tm.member), domain.ActionNoteEdit, string(note)) {
		t.Fatal("re-invited member inherited a stale override")
	}
	wantErr(t, h.svc.RemoveMember(ctx, subj(tm.admin), tm.ws, tm.outsider.ID), domain.ErrNotFound)
	wantErr(t, h.svc.RemoveMember(ctx, subj(tm.member), tm.ws, tm.guest.ID), domain.ErrPermissionDenied)
}

func TestNoteRolesLowerMembersGrantGuestsAndPublishWhatChanged(t *testing.T) {
	tm := newTeam(t)
	h := tm.h
	note := h.note(tm.ws, tm.member)
	set := func(actor domain.User, target domain.User, role domain.NoteRole) error {
		err := h.svc.SetNoteRole(ctx, subj(actor), note, target.ID, role)
		h.check()
		return err
	}
	if err := set(tm.member, tm.member, domain.NoteRoleViewer); err != nil {
		t.Fatal(err)
	}
	if h.can(subj(tm.member), domain.ActionNoteEdit, string(note)) || !h.can(subj(tm.member), domain.ActionNoteRead, string(note)) {
		t.Fatal("viewer override not in force")
	}
	// Now a viewer, the member cannot share any more.
	wantErr(t, set(tm.member, tm.guest, domain.NoteRoleViewer), domain.ErrPermissionDenied)
	if err := set(tm.owner, tm.guest, domain.NoteRoleEditor); err != nil {
		t.Fatal(err)
	}
	if !h.can(subj(tm.guest), domain.ActionNoteEdit, string(note)) || h.can(subj(tm.guest), domain.ActionNoteShare, string(note)) {
		t.Fatal("guest editor: may edit, may not share")
	}
	if err := set(tm.owner, tm.member, domain.NoteRoleNone); err != nil {
		t.Fatal(err)
	}
	if !h.can(subj(tm.member), domain.ActionNoteEdit, string(note)) {
		t.Fatal("clearing the override did not restore editor")
	}
	if err := set(tm.owner, tm.guest, domain.NoteRoleNone); err != nil {
		t.Fatal(err)
	}
	if h.can(subj(tm.guest), domain.ActionNoteRead, string(note)) {
		t.Fatal("clearing a guest's grant left access")
	}
	ev := h.store.Published()
	revoked := []bool{}
	for _, e := range ev {
		if e.NoteID != note {
			t.Fatalf("event without the note: %+v", e)
		}
		revoked = append(revoked, e.Revoked)
	}
	if fmt.Sprint(revoked) != "[true false false true]" {
		t.Fatalf("revoked flags %v: lowering and clearing a guest revoke; granting and restoring do not", revoked)
	}
	h.store.ResetPublished()
	if err := set(tm.owner, tm.member, domain.NoteRoleNone); err != nil || len(h.store.Published()) != 0 {
		t.Fatalf("clearing an absent override: %v, %d events", err, len(h.store.Published()))
	}
	wantErr(t, set(tm.owner, tm.admin, domain.NoteRoleViewer), domain.ErrFailedPrecondition)
	wantErr(t, set(tm.owner, tm.outsider, domain.NoteRoleViewer), domain.ErrFailedPrecondition)
	wantErr(t, set(tm.outsider, tm.member, domain.NoteRoleViewer), domain.ErrNotFound)
	wantErr(t, set(tm.guest, tm.member, domain.NoteRoleViewer), domain.ErrNotFound)
	if err := h.svc.SetNoteRole(ctx, subj(tm.owner), note, tm.member.ID, 9); fieldOf(err) != "role" {
		t.Fatalf("role out of range: %v", err)
	}
}

func TestShareLinksAreViewerOrCommenterExpireAndRevoke(t *testing.T) {
	tm := newTeam(t)
	h := tm.h
	note := h.note(tm.ws, tm.member)
	_, _, err := h.svc.CreateShareLink(ctx, subj(tm.member), note, domain.NoteRoleEditor, nil)
	if fieldOf(err) != "role" {
		t.Fatalf("editor link: %v", err)
	}
	id, token, err := h.svc.CreateShareLink(ctx, subj(tm.member), note, domain.NoteRoleCommenter, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.check()
	links := h.store.ShareLinks()
	if len(links) != 1 || links[0].TokenHash != credential.HashToken(token) || links[0].ExpiresAt == nil ||
		!links[0].ExpiresAt.Equal(h.clock.Now().Add(7*24*time.Hour)) {
		t.Fatalf("stored link %+v", links)
	}
	holder, linkNote, err := h.svc.ResolveShareLink(ctx, token)
	if err != nil || linkNote != note || holder.ShareLinkID != id {
		t.Fatalf("resolve: %+v %s %v", holder, linkNote, err)
	}
	if !h.can(holder, domain.ActionNoteComment, string(note)) || h.can(holder, domain.ActionNoteEdit, string(note)) {
		t.Fatal("commenter link: may comment, not edit")
	}
	if ev := h.store.Published(); len(ev) != 1 || ev[0].NoteID != note || ev[0].UserID != "" || ev[0].Revoked {
		t.Fatalf("create events %+v", ev)
	}
	wantErr(t, h.svc.RevokeShareLink(ctx, subj(tm.guest), id), domain.ErrNotFound)
	wantErr(t, h.svc.RevokeShareLink(ctx, subj(tm.outsider), id), domain.ErrNotFound)
	if err := h.svc.RevokeShareLink(ctx, subj(tm.owner), id); err != nil {
		t.Fatal(err)
	}
	if h.can(holder, domain.ActionNoteRead, string(note)) {
		t.Fatal("revoked link still works: cache not invalidated")
	}
	if err := h.svc.RevokeShareLink(ctx, subj(tm.owner), id); err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if ev := h.store.Published(); len(ev) != 2 || !ev[1].Revoked {
		t.Fatalf("revoke events %+v (want exactly one revocation)", ev)
	}

	later := h.clock.Now().Add(time.Hour)
	_, token2, err := h.svc.CreateShareLink(ctx, subj(tm.owner), note, domain.NoteRoleViewer, &later)
	if err != nil {
		t.Fatal(err)
	}
	holder2, _, _ := h.svc.ResolveShareLink(ctx, token2)
	if !h.can(holder2, domain.ActionNoteRead, string(note)) {
		t.Fatal("fresh viewer link cannot read")
	}
	h.clock.Advance(time.Hour + h.cfg.DecisionTTL)
	if h.can(holder2, domain.ActionNoteRead, string(note)) {
		t.Fatal("expired link still works")
	}
	_, _, err = h.svc.CreateShareLink(ctx, subj(tm.guest), note, domain.NoteRoleViewer, nil)
	wantErr(t, err, domain.ErrNotFound)
	_, _, err = h.svc.ResolveShareLink(ctx, "sl_unknown")
	wantErr(t, err, domain.ErrUnauthenticated)
}

func TestShareLinksWithoutDefaultLifetimeNeverExpire(t *testing.T) {
	tm := newTeam(t, withConfig(func(c *app.Config) { c.ShareLinkDefaultTTL = 0 }))
	note := tm.h.note(tm.ws, tm.member)
	if _, _, err := tm.h.svc.CreateShareLink(ctx, subj(tm.member), note, domain.NoteRoleViewer, nil); err != nil {
		t.Fatal(err)
	}
	if l := tm.h.store.ShareLinks(); l[0].ExpiresAt != nil {
		t.Fatalf("expires at %v", l[0].ExpiresAt)
	}
}

func TestNoteEventsUpdateTheProjectionOnceAndInvalidateDecisions(t *testing.T) {
	tm := newTeam(t)
	h := tm.h
	id, _ := domain.NewID()
	note := domain.NoteID(id)
	// Before documents' event arrives the note is unknown, and that answer
	// must not be cached.
	if h.can(subj(tm.member), domain.ActionNoteRead, id) {
		t.Fatal("unknown note readable")
	}
	created := app.NoteEvent{Kind: app.NoteCreated, NoteID: note, WorkspaceID: tm.ws, Actor: tm.member.ID, OccurredAt: h.clock.Now()}
	if out, err := h.svc.ApplyNoteEvent(ctx, "c", "e1", created); err != nil || out != app.OutcomeApplied {
		t.Fatalf("%v %v", out, err)
	}
	if !h.can(subj(tm.member), domain.ActionNoteDelete, id) {
		t.Fatal("creator cannot delete right after NoteCreated: an unknown-note deny was cached")
	}
	trash := app.NoteEvent{Kind: app.NoteTrashed, NoteID: note, OccurredAt: h.clock.Now()}
	if _, err := h.svc.ApplyNoteEvent(ctx, "c", "e2", trash); err != nil {
		t.Fatal(err)
	}
	if h.can(subj(tm.member), domain.ActionNoteRead, id) || !h.can(subj(tm.admin), domain.ActionNoteRead, id) {
		t.Fatal("trash: only owners and admins read")
	}
	restore := app.NoteEvent{Kind: app.NoteRestored, NoteID: note}
	if _, err := h.svc.ApplyNoteEvent(ctx, "c", "e3", restore); err != nil {
		t.Fatal(err)
	}
	// Replaying the trash event is a duplicate and changes nothing.
	if out, err := h.svc.ApplyNoteEvent(ctx, "c", "e2", trash); err != nil || out != app.OutcomeDuplicate {
		t.Fatalf("replay: %v %v", out, err)
	}
	if !h.can(subj(tm.member), domain.ActionNoteRead, id) {
		t.Fatal("a duplicate delivery re-trashed the note")
	}
	if _, _, err := h.svc.CreateShareLink(ctx, subj(tm.member), note, domain.NoteRoleViewer, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ApplyNoteEvent(ctx, "c", "e4", app.NoteEvent{Kind: app.NotePurged, NoteID: note}); err != nil {
		t.Fatal(err)
	}
	if h.can(subj(tm.owner), domain.ActionNoteRead, id) || len(h.store.ShareLinks()) != 0 {
		t.Fatal("purge left the note or its links")
	}
	if _, err := h.svc.ApplyNoteEvent(ctx, "c", "e5", app.NoteEvent{Kind: app.NoteTrashed, NoteID: "unknown"}); err != nil {
		t.Fatalf("event for an unknown note: %v", err)
	}
}

func TestAuthorizeBatchesAndCapsTheBatchSize(t *testing.T) {
	tm := newTeam(t, withConfig(func(c *app.Config) { c.MaxChecksPerAuthorize = 3 }))
	h := tm.h
	note := h.note(tm.ws, tm.owner)
	got, err := h.svc.Authorize(ctx, subj(tm.guest), []domain.Check{
		{Action: domain.ActionNoteCreate, ResourceID: string(tm.ws)},
		{Action: domain.ActionNoteRead, ResourceID: string(note)},
		{Action: domain.ActionNoteRead, ResourceID: "not-a-uuid"},
	})
	if err != nil || len(got) != 3 || got[0].Allowed || got[1].Allowed || got[2].Allowed {
		t.Fatalf("%+v %v", got, err)
	}
	_, err = h.svc.Authorize(ctx, subj(tm.guest), make([]domain.Check, 4))
	if fieldOf(err) != "checks" {
		t.Fatalf("oversized batch: %v", err)
	}
	agent := domain.UserSubject(tm.member.ID, "019d06e4-080e-74b5-8cd7-d007c93937ce")
	if !h.can(agent, domain.ActionNoteEdit, string(note)) || h.can(agent, domain.ActionManageMembers, string(tm.ws)) {
		t.Fatal("an agent run is decided as its user")
	}
}

func TestListReadableNotesIsWhatAuthorizeAllows(t *testing.T) {
	tm := newTeam(t)
	h := tm.h
	a, b, c := h.note(tm.ws, tm.owner), h.note(tm.ws, tm.member), h.note(tm.ws, tm.member)
	if err := h.svc.SetNoteRole(ctx, subj(tm.owner), b, tm.guest.ID, domain.NoteRoleViewer); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ApplyNoteEvent(ctx, "c", "trash-c", app.NoteEvent{Kind: app.NoteTrashed, NoteID: c, OccurredAt: h.clock.Now()}); err != nil {
		t.Fatal(err)
	}
	cases := map[domain.UserID][]domain.NoteID{
		tm.owner.ID: {a, b, c}, tm.member.ID: {a, b}, tm.guest.ID: {b}, tm.outsider.ID: nil,
	}
	for user, want := range cases {
		got, err := h.svc.ListReadableNotes(ctx, domain.UserSubject(user, ""), tm.ws)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: %v %v, want %v", user, got, err, want)
		}
	}
	if got, _ := h.svc.ListReadableNotes(ctx, subj(tm.owner), "019b7892-1c00-75eb-b4c7-000000000000"); len(got) != 0 {
		t.Errorf("unknown workspace lists %v", got)
	}
}

func TestInternalCallersMayOnlyAskAboutThemselves(t *testing.T) {
	h := newHarness(t)
	self := domain.UserSubject("u1", "")
	run := domain.UserSubject("u1", "r1")
	if err := h.svc.CheckInternalCaller(&self, self); err != nil {
		t.Error(err)
	}
	if err := h.svc.CheckInternalCaller(&run, run); err != nil {
		t.Error(err)
	}
	other := domain.UserSubject("u2", "")
	wantErr(t, h.svc.CheckInternalCaller(&self, other), domain.ErrPermissionDenied)
	otherRun := domain.UserSubject("u1", "r2")
	wantErr(t, h.svc.CheckInternalCaller(&run, otherRun), domain.ErrPermissionDenied)
	wantErr(t, h.svc.CheckInternalCaller(nil, self), domain.ErrUnauthenticated)
	open := newHarness(t, withConfig(func(c *app.Config) { c.AllowUnauthenticatedInternalCalls = true }))
	if err := open.svc.CheckInternalCaller(nil, other); err != nil {
		t.Errorf("token-less call with the switch on: %v", err)
	}
}

func TestDelegatedTokensCarryTheRunAreShortAndOnlyForYourself(t *testing.T) {
	tm := newTeam(t)
	h := tm.h
	run := domain.AgentRunID("019d06e4-080e-74b5-8cd7-d007c93937ce")
	tok, exp, err := h.svc.IssueDelegatedToken(ctx, subj(tm.member), tm.member.ID, run, nil)
	if err != nil {
		t.Fatal(err)
	}
	issued, _ := h.issuer.Lookup(tok)
	if issued.Subject.UserID != tm.member.ID || issued.Subject.AgentRunID != run || !exp.Equal(h.clock.Now().Add(15*time.Minute)) {
		t.Fatalf("issued %+v until %v", issued, exp)
	}
	soon := h.clock.Now().Add(90 * time.Second)
	if _, exp, _ := h.svc.IssueDelegatedToken(ctx, subj(tm.member), tm.member.ID, run, &soon); !exp.Equal(soon) {
		t.Errorf("requested expiry %v, got %v", soon, exp)
	}
	far := h.clock.Now().Add(24 * time.Hour)
	if _, exp, _ := h.svc.IssueDelegatedToken(ctx, subj(tm.member), tm.member.ID, run, &far); !exp.Equal(h.clock.Now().Add(15 * time.Minute)) {
		t.Errorf("expiry not capped: %v", exp)
	}
	_, _, err = h.svc.IssueDelegatedToken(ctx, subj(tm.owner), tm.member.ID, run, nil)
	wantErr(t, err, domain.ErrPermissionDenied)
	_, _, err = h.svc.IssueDelegatedToken(ctx, domain.UserSubject(tm.member.ID, run), tm.member.ID, run, nil)
	wantErr(t, err, domain.ErrPermissionDenied)
	past := h.clock.Now().Add(-time.Second)
	_, _, err = h.svc.IssueDelegatedToken(ctx, subj(tm.member), tm.member.ID, run, &past)
	if fieldOf(err) != "expires_at" {
		t.Fatalf("past expiry: %v", err)
	}
}

func TestReplicasBelongToWhoeverRegisteredThemFirst(t *testing.T) {
	tm := newTeam(t)
	h := tm.h
	const id = domain.ReplicaID(4831727201934127107)
	if err := h.svc.RegisterReplica(ctx, subj(tm.member), id); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RegisterReplica(ctx, subj(tm.member), id); err != nil {
		t.Fatalf("re-registering one's own replica: %v", err)
	}
	wantErr(t, h.svc.RegisterReplica(ctx, subj(tm.owner), id), domain.ErrAlreadyExists)
	agent := domain.UserSubject(tm.owner.ID, "019d06e4-080e-74b5-8cd7-d007c93937ce")
	if err := h.svc.RegisterReplica(ctx, agent, id+1); err != nil {
		t.Fatal(err)
	}
	h.check()
}

func TestGetUserOnlyShowsPeopleYouShareAWorkspaceWith(t *testing.T) {
	tm := newTeam(t)
	h := tm.h
	if u, err := h.svc.GetUser(ctx, subj(tm.guest), tm.owner.ID); err != nil || u.ID != tm.owner.ID {
		t.Fatalf("%+v %v", u, err)
	}
	if _, err := h.svc.GetUser(ctx, subj(tm.outsider), tm.outsider.ID); err != nil {
		t.Fatalf("self: %v", err)
	}
	_, err := h.svc.GetUser(ctx, subj(tm.outsider), tm.owner.ID)
	wantErr(t, err, domain.ErrNotFound)
	_, err = h.svc.GetUser(ctx, domain.Subject{}, tm.owner.ID)
	wantErr(t, err, domain.ErrUnauthenticated)
}

// Documents registers a note right after creating it, so nobody depends on
// the NoteCreated event having reached the projection to use it.
func TestRegisteredNoteIsUsableAtOnceAndCannotBeMoved(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.signUp("owner@example.com")
	member, _ := h.signUp("member@example.com")
	guest, _ := h.signUp("guest@example.com")
	ws := h.workspace(owner)
	other := h.workspace(owner)
	h.setRole(owner, ws, member, domain.WorkspaceRoleMember)
	h.setRole(owner, ws, guest, domain.WorkspaceRoleGuest)
	id, _ := domain.NewID()
	note := domain.NoteID(id)

	if h.can(subj(member), domain.ActionNoteRead, string(note)) {
		t.Fatal("an unknown note is readable")
	}
	if err := h.svc.RegisterNote(ctx, subj(member), note, ws); err != nil {
		t.Fatal(err)
	}
	h.check()
	for _, u := range []domain.User{member, owner} {
		if !h.can(subj(u), domain.ActionNoteEdit, string(note)) {
			t.Errorf("%s cannot edit the note just registered", u.Email)
		}
	}
	if h.can(subj(guest), domain.ActionNoteRead, string(note)) {
		t.Error("a guest can read a note nobody shared")
	}
	if !h.can(subj(member), domain.ActionNoteDelete, string(note)) {
		t.Error("the registering member is not the creator")
	}

	// Idempotent in the same workspace, refused in another, and the event
	// arriving later agrees with the registration.
	if err := h.svc.RegisterNote(ctx, subj(owner), note, ws); err != nil {
		t.Fatalf("registering again: %v", err)
	}
	wantErr(t, h.svc.RegisterNote(ctx, subj(owner), note, other), domain.ErrAlreadyExists)
	if _, err := h.svc.ApplyNoteEvent(ctx, "identity.documents-events", "evt-"+id, app.NoteEvent{
		Kind: app.NoteCreated, NoteID: note, WorkspaceID: ws, Actor: member.ID, OccurredAt: h.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if !h.can(subj(member), domain.ActionNoteDelete, string(note)) {
		t.Error("the NoteCreated event changed the registered note")
	}

	id2, _ := domain.NewID()
	wantErr(t, h.svc.RegisterNote(ctx, subj(guest), domain.NoteID(id2), ws), domain.ErrPermissionDenied)
	if h.can(subj(owner), domain.ActionNoteRead, id2) {
		t.Error("a refused registration recorded the note")
	}
}

// The token-less variants are for event consumers only, and only when the
// internal-call flag is on.
func TestInternalReadsAreRefusedUnlessTheFlagIsSet(t *testing.T) {
	h := newHarness(t)
	owner, _ := h.signUp("owner@example.com")
	ws := h.workspace(owner)
	if _, err := h.svc.GetUserInternal(ctx, owner.ID); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("GetUserInternal: %v", err)
	}
	if _, err := h.svc.ListMembersInternal(ctx, ws, 0, ""); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Errorf("ListMembersInternal: %v", err)
	}
	on := newHarness(t, withConfig(func(c *app.Config) { c.AllowUnauthenticatedInternalCalls = true }))
	owner, _ = on.signUp("owner@example.com")
	ws = on.workspace(owner)
	if u, err := on.svc.GetUserInternal(ctx, owner.ID); err != nil || u.ID != owner.ID {
		t.Errorf("GetUserInternal with the flag: %v %v", u, err)
	}
	if p, err := on.svc.ListMembersInternal(ctx, ws, 0, ""); err != nil || len(p.Members) != 1 {
		t.Errorf("ListMembersInternal with the flag: %+v %v", p, err)
	}
}
