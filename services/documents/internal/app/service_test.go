package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/libs/dp"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/fakes"
)

var ctx = context.Background()

func TestNoteLifecyclePublishesEventsInOrder(t *testing.T) {
	e := newEnv(t, Config{})
	id := e.newNote(t, "# Raft log compaction\nSnapshots bound the log. See [[Raft overview]].")
	note, md, vv, err := e.svc.GetNote(ctx, alice, id, true)
	if err != nil {
		t.Fatal(err)
	}
	if md != "# Raft log compaction\n\nSnapshots bound the log. See [[Raft overview]]." || note.Title != "Raft log compaction" || vv.Get(domain.ServerReplica) == 0 {
		t.Fatalf("got %q %+v %v", md, note, vv)
	}
	if err := e.svc.RenameNote(ctx, bob, id, "Raft snapshots"); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.RenameNote(ctx, bob, id, "Raft snapshots"); err != nil {
		t.Fatal("renaming to the same title succeeds")
	}
	mustErr(t, e.svc.DeleteNote(ctx, bob, id), domain.ErrPermissionDenied)
	if err := e.svc.DeleteNote(ctx, alice, id); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteNote(ctx, alice, id); err != nil {
		t.Fatal("deleting a trashed note succeeds")
	}
	mustErr(t, e.svc.RenameNote(ctx, alice, id, "x"), domain.ErrFailedPrecondition)
	if err := e.svc.RestoreNote(ctx, alice, id); err != nil {
		t.Fatal(err)
	}
	want := []domain.EventKind{domain.EventNoteCreated, domain.EventBlocksChanged, domain.EventLinksChanged, domain.EventNoteRenamed, domain.EventNoteTrashed, domain.EventNoteRestored}
	events := e.store.Outbox()
	if got := eventKinds(events); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	if !slices.Equal(events[2].Links, []string{"Raft overview"}) || events[3].Title != "Raft snapshots" || events[3].Actor != bob {
		t.Fatalf("payloads %+v", events)
	}
	for _, ev := range events {
		if ev.NoteID != id {
			t.Fatalf("event %v for note %s", ev.Kind, ev.NoteID)
		}
	}
}

func TestUnreadableNotesAreNotFoundAndViewersCannotEdit(t *testing.T) {
	e := newEnv(t, Config{})
	id := e.newNote(t, "")
	stranger := domain.Subject{UserID: "mallory"}
	_, _, _, err := e.svc.GetNote(ctx, stranger, id, false)
	mustErr(t, err, domain.ErrNotFound)
	_, _, _, err = e.svc.GetNote(ctx, alice, "019c8cf2-0000-7000-8000-000000000000", false)
	mustErr(t, err, domain.ErrNotFound)

	e.auth.SetRole(workspace, id, "victor", fakes.Viewer)
	viewer := domain.Subject{UserID: "victor"}
	seq, _ := crdt.New(1 << 40)
	ops, _ := seq.Insert(0, "x")
	_, err = e.svc.ApplyOps(ctx, viewer, id, 1<<40, crdt.EncodeOps(ops))
	mustErr(t, err, domain.ErrPermissionDenied)
	_, err = e.svc.ApplyOps(ctx, stranger, id, 1<<40, crdt.EncodeOps(ops))
	mustErr(t, err, domain.ErrNotFound)
}

func TestReplicaOfAnotherUserIsRefused(t *testing.T) {
	e := newEnv(t, Config{})
	id := e.newNote(t, "")
	e.replicas.Register(1<<40, alice.UserID)
	seq, _ := crdt.New(1 << 40)
	ops, _ := seq.Insert(0, "forged")
	_, err := e.svc.ApplyOps(ctx, bob, id, 1<<40, crdt.EncodeOps(ops))
	mustErr(t, err, domain.ErrPermissionDenied)
	_, err = e.svc.ApplyOps(ctx, bob, id, domain.ServerReplica, crdt.EncodeOps(ops))
	mustErr(t, err, domain.ErrInvalidArgument)
	_, err = e.svc.OpenSession(ctx, bob, Hello{Note: id, Replica: 1 << 40})
	mustErr(t, err, domain.ErrPermissionDenied)
	if got := e.text(t, id); got != "" {
		t.Fatalf("forged ops applied: %q", got)
	}
}

func TestApplyOpsIsIdempotentAndHoldsOutOfOrderOps(t *testing.T) {
	e := newEnv(t, Config{})
	id := e.newNote(t, "")
	seq, _ := crdt.New(1 << 40)
	ops, _ := seq.Insert(0, "\u001ehello")
	vv, err := e.svc.ApplyOps(ctx, bob, id, 1<<40, crdt.EncodeOps(ops[3:]))
	if err != nil || vv.Get(1<<40) != 0 {
		t.Fatalf("out-of-order ops must wait: %v %v", vv, err)
	}
	for range 2 {
		if vv, err = e.svc.ApplyOps(ctx, bob, id, 1<<40, crdt.EncodeOps(ops)); err != nil {
			t.Fatal(err)
		}
	}
	if vv.Get(1<<40) != 6 || e.text(t, id) != "\u001ehello" {
		t.Fatalf("vv %v text %q", vv, e.text(t, id))
	}
	if n := len(e.store.Batches(id)); n != 1 {
		t.Fatalf("%d batches: a resent batch must add nothing", n)
	}
	reused := ops[0]
	reused.Value = 'X'
	_, err = e.svc.ApplyOps(ctx, bob, id, 1<<40, crdt.EncodeOps([]crdt.Op{reused}))
	mustErr(t, err, domain.ErrInvalidArgument)
}

func TestListNotesPaginatesReadableNotesAndRejectsForeignTokens(t *testing.T) {
	e := newEnv(t, Config{})
	var ids []domain.NoteID
	for range 5 {
		ids = append(ids, e.newNote(t, ""))
	}
	e.auth.SetRole(workspace, ids[2], bob.UserID, fakes.NoAccess)
	if err := e.svc.DeleteNote(ctx, alice, ids[4]); err != nil {
		t.Fatal(err)
	}
	var got []domain.NoteID
	token := ""
	for {
		notes, next, err := e.svc.ListNotes(ctx, bob, workspace, 2, token, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range notes {
			got = append(got, n.ID)
		}
		if next == "" {
			break
		}
		token = next
	}
	if want := []domain.NoteID{ids[0], ids[1], ids[3]}; !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	_, next, _ := e.svc.ListNotes(ctx, alice, workspace, 1, "", false)
	_, _, err := e.svc.ListNotes(ctx, alice, workspace, 1, next, true)
	mustErr(t, err, domain.ErrInvalidArgument)
}

// Convergence through the service: clients edit concurrently, send batches
// split and out of order, resend after reconnecting, go offline, and receive
// the server's frames late. Every client and the server end with one text.
func TestSyncConvergesWithManyUnreliableClients(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	for round := 0; round < 15; round++ {
		e := newEnv(t, Config{})
		id := e.newNote(t, "")
		users := []domain.Subject{alice, bob, carol, bob}
		var clients []*client
		for i, u := range users {
			c := e.client(t, id, u, crdt.ReplicaID(1<<40+uint64(i)))
			if err := c.connect(); err != nil {
				t.Fatal(err)
			}
			clients = append(clients, c)
		}
		for step := 0; step < 60; step++ {
			c := clients[r.IntN(len(clients))]
			switch k := r.IntN(10); {
			case k == 0 && c.sess != nil:
				c.disconnect() // goes offline, keeps editing locally
			case k == 1 && c.sess == nil:
				if err := c.connect(); err != nil {
					t.Fatal(err)
				}
			case k == 2:
				c.pump()
			default:
				var ops []crdt.Op
				if c.seq.Len() > 0 && r.IntN(3) == 0 {
					i := r.IntN(c.seq.Len())
					ops = c.delete(i, 1+r.IntN(min(3, c.seq.Len()-i)))
				} else {
					ops = c.insert(r.IntN(c.seq.Len()+1), []string{"ab", "\u001e", "漢🙂", "x"}[r.IntN(4)])
				}
				if c.sess == nil {
					continue
				}
				// Send the batch in two parts, the later part first, and
				// sometimes twice.
				cut := r.IntN(len(ops) + 1)
				if cut < len(ops) {
					c.submit(ops[cut:])
				}
				if cut > 0 {
					c.submit(ops[:cut])
				}
				if r.IntN(5) == 0 {
					c.submit(ops)
				}
			}
		}
		for _, c := range clients {
			if c.sess == nil {
				if err := c.connect(); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, c := range clients {
			c.pump()
		}
		server := e.text(t, id)
		for _, c := range clients {
			if c.seq.Text() != server {
				t.Fatalf("round %d: client %d has %q, server %q", round, c.seq.Replica(), c.seq.Text(), server)
			}
			if len(c.rejected) > 0 {
				t.Fatalf("client %d: unexpected rejections %+v", c.seq.Replica(), c.rejected)
			}
			if p := c.buf.Pending(); p != 0 {
				t.Fatalf("client %d holds %d ops", c.seq.Replica(), p)
			}
		}
		// The op log alone rebuilds the same note.
		reloaded := e.restart(t, Config{})
		if got := reloaded.text(t, id); got != server {
			t.Fatalf("round %d: reload gives %q, want %q", round, got, server)
		}
	}
}

func TestRevokedAccessMidSessionRejectsBatchesThenClosesTheStream(t *testing.T) {
	e := newEnv(t, Config{})
	id := e.newNote(t, "")
	c := e.client(t, id, bob, 1<<40)
	if err := c.connect(); err != nil {
		t.Fatal(err)
	}
	c.submit(c.insert(0, "\u001eallowed"))
	e.auth.SetRole(workspace, id, bob.UserID, fakes.Viewer)
	c.submit(c.insert(8, " lowered"))
	c.pump()
	if len(c.rejected) != 1 || c.rejected[0].Reason != ReasonInsufficientRole {
		t.Fatalf("rejections %+v", c.rejected)
	}
	if got := e.text(t, id); got != "\u001eallowed" {
		t.Fatalf("server text %q: a viewer's batch must not apply", got)
	}
	e.auth.SetRole(workspace, id, bob.UserID, fakes.NoAccess)
	if err := e.svc.AccessChanged(ctx, workspace, id, bob.UserID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.sess.Done():
		mustErr(t, c.sess.Err(), domain.ErrPermissionDenied)
	default:
		t.Fatal("a revoked user's stream must close")
	}
}

func TestUndoTouchesOnlyTheReplicasOwnEdits(t *testing.T) {
	e := newEnv(t, Config{UndoWindow: time.Second})
	id := e.newNote(t, "")
	a := e.client(t, id, alice, 1<<40)
	b := e.client(t, id, bob, 1<<41)
	for _, c := range []*client{a, b} {
		if err := c.connect(); err != nil {
			t.Fatal(err)
		}
	}
	a.submit(a.insert(0, "\u001eraft "))
	b.pump()
	e.clock.Advance(time.Minute)
	b.submit(b.insert(6, "log "))
	a.pump()
	e.clock.Advance(time.Minute)
	a.submit(a.insert(10, "compaction"))
	b.pump()
	if got := e.text(t, id); got != "\u001eraft log compaction" {
		t.Fatalf("text %q", got)
	}
	if _, err := e.svc.Undo(ctx, alice, id, 1<<40); err != nil {
		t.Fatal(err)
	}
	if got := e.text(t, id); got != "\u001eraft log " {
		t.Fatalf("after one undo %q", got)
	}
	if _, err := e.svc.Undo(ctx, alice, id, 1<<40); err != nil {
		t.Fatal(err)
	}
	if got := e.text(t, id); got != "log " {
		t.Fatalf("after two undos %q: bob's text must stay", got)
	}
	_, err := e.svc.Undo(ctx, alice, id, 1<<40)
	mustErr(t, err, domain.ErrFailedPrecondition)
	_, err = e.svc.Undo(ctx, bob, id, 1<<40)
	mustErr(t, err, domain.ErrPermissionDenied)
	if _, err := e.svc.Redo(ctx, alice, id, 1<<40); err != nil {
		t.Fatal(err)
	}
	if got := e.text(t, id); got != "\u001eraft log " {
		t.Fatalf("after redo %q", got)
	}
	// Clients receive the server's undo ops like any others.
	a.pump()
	b.pump()
	if a.seq.Text() != e.text(t, id) || b.seq.Text() != e.text(t, id) {
		t.Fatalf("clients %q %q, server %q", a.seq.Text(), b.seq.Text(), e.text(t, id))
	}
	// Undo state survives a restart: it is derived from the op log.
	e2 := e.restart(t, Config{UndoWindow: time.Second})
	if _, err := e2.svc.Redo(ctx, alice, id, 1<<40); err != nil {
		t.Fatal(err)
	}
	if got := e2.text(t, id); got != "\u001eraft log compaction" {
		t.Fatalf("redo after restart %q", got)
	}
}

func TestAgentSuggestionIsUndoneAsOneGroup(t *testing.T) {
	e := newEnv(t, Config{UndoWindow: time.Hour})
	id := e.newNote(t, "Snapshots bound the log.")
	agent := domain.Subject{UserID: alice.UserID, AgentRunID: "run-1"}
	_, _, vv, _ := e.svc.GetNote(ctx, alice, id, true)
	seq, _ := crdt.New(1 << 42)
	var ops []crdt.Op
	e.state(t, id, func(st *noteState) { ops = st.doc.Sequence().OpsSince(crdt.VersionVector{}) })
	for _, op := range ops {
		_ = seq.Apply(op)
	}
	_ = vv
	first, _ := seq.Insert(24, " and let slow followers")
	second, _ := seq.Insert(seq.Len()-1, " catch up")
	all := append(first, second...)
	if _, err := e.svc.ApplyOps(ctx, agent, id, 1<<42, crdt.EncodeOps(all)); err != nil {
		t.Fatal(err)
	}
	spans, err := e.svc.Blame(ctx, alice, id)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(spans, func(s domain.BlameSpan) bool { return s.AgentRunID == "run-1" && s.End-s.Start == 32 }) {
		t.Fatalf("blame %+v: the agent's 32 code points must be credited to its run", spans)
	}
	if _, err := e.svc.Undo(ctx, alice, id, 1<<42); err != nil {
		t.Fatal(err)
	}
	if got := e.text(t, id); got != "\u001eSnapshots bound the log." {
		t.Fatalf("after rejecting the suggestion %q", got)
	}
}

func TestCrashAfterCommitReloadsAcknowledgedStateAndRepublishes(t *testing.T) {
	e := newEnv(t, Config{})
	id := e.newNote(t, "")
	seq, _ := crdt.New(1 << 40)
	ops, _ := seq.Insert(0, "\u001eSee [[Raft overview]]")
	if _, err := e.svc.ApplyOps(ctx, bob, id, 1<<40, crdt.EncodeOps(ops)); err != nil {
		t.Fatal(err)
	}
	before := len(e.store.Outbox())
	// Crash before the debounced events were published.
	e2 := e.restart(t, Config{})
	if got := e2.text(t, id); got != "\u001eSee [[Raft overview]]" {
		t.Fatalf("reloaded %q", got)
	}
	e2.svc.kill()
	e3 := newEnvOn(t, e.store, e.auth, e.replicas, e.clock, Config{})
	if err := e3.svc.RecoverProjections(ctx); err != nil {
		t.Fatal(err)
	}
	events := e.store.Outbox()[before:]
	if got := eventKinds(events); !slices.Equal(got, []domain.EventKind{domain.EventBlocksChanged, domain.EventLinksChanged}) {
		t.Fatalf("recovered events %v", got)
	}
	if stale, _ := e.store.StaleProjections(ctx, 10); len(stale) != 0 {
		t.Fatalf("still stale: %v", stale)
	}
}

func TestFailedAppendAcknowledgesNothingAndReloads(t *testing.T) {
	e := newEnv(t, Config{})
	id := e.newNote(t, "")
	c := e.client(t, id, carol, 1<<43)
	watcher := e.client(t, id, bob, 1<<41)
	if err := watcher.connect(); err != nil {
		t.Fatal(err)
	}
	seq, _ := crdt.New(1 << 40)
	ops, _ := seq.Insert(0, "\u001elost?")
	fail := true
	e.store.FailAppend = func(domain.NoteID, []domain.Batch) error {
		if fail {
			fail = false
			return domain.ErrUnavailable
		}
		return nil
	}
	_, err := e.svc.ApplyOps(ctx, bob, id, 1<<40, crdt.EncodeOps(ops))
	mustErr(t, err, domain.ErrUnavailable)
	select {
	case f := <-watcher.sess.Frames():
		t.Fatalf("an unpersisted batch was broadcast: %+v", f)
	default:
	}
	if len(e.store.Batches(id)) != 0 {
		t.Fatal("nothing may be stored")
	}
	if _, err := e.svc.ApplyOps(ctx, bob, id, 1<<40, crdt.EncodeOps(ops)); err != nil {
		t.Fatal(err)
	}
	if got := e.text(t, id); got != "\u001elost?" {
		t.Fatalf("after retry %q", got)
	}
	_ = c
}

func TestSnapshotsBoundReplayAndReloadIdentically(t *testing.T) {
	e := newEnv(t, Config{SnapshotEvery: 50})
	id := e.newNote(t, "")
	c := e.client(t, id, bob, 1<<40)
	if err := c.connect(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		c.submit(c.insert(c.seq.Len(), "abcdefgh"[:1+i%8]))
		if i%5 == 0 {
			c.submit(c.delete(0, 1))
		}
	}
	want := e.text(t, id)
	if e.store.Snapshots(id) == 0 {
		t.Fatal("no snapshot was taken")
	}
	e2 := e.restart(t, Config{SnapshotEvery: 50})
	if got := e2.text(t, id); got != want {
		t.Fatalf("reload from snapshot gives %q, want %q", got, want)
	}
	if !e2.version(t, id).Equal(c.seq.Version()) {
		t.Fatal("version vectors differ after reload")
	}
}

func TestHistoryVersionsTextAtAndDiffAgreeWithWhatWasWritten(t *testing.T) {
	e := newEnv(t, Config{VersionWindow: time.Minute})
	id := e.newNote(t, "# Raft log compaction\nSnapshots bound the log.")
	c := e.client(t, id, bob, 1<<40)
	if err := c.connect(); err != nil {
		t.Fatal(err)
	}
	c.pump()
	type point struct {
		vv crdt.VersionVector
		md string
	}
	record := func() point {
		_, md, vv, err := e.svc.GetNote(ctx, alice, id, true)
		if err != nil {
			t.Fatal(err)
		}
		return point{vv, md}
	}
	points := []point{record()}
	edits := []func(){
		func() { c.submit(c.insert(c.seq.Len()-1, " and let slow followers catch up")) },
		func() { c.submit(c.insert(c.seq.Len(), "\u001eA second paragraph.")) },
		func() { c.submit(c.delete(1, 4)) },
	}
	for _, edit := range edits {
		e.clock.Advance(10 * time.Minute)
		edit()
		points = append(points, record())
	}
	versions, next, err := e.svc.ListVersions(ctx, alice, id, 0, "")
	if err != nil || next != "" || len(versions) != len(points) {
		t.Fatalf("%d versions, err %v", len(versions), err)
	}
	for i, v := range versions {
		p := points[len(points)-1-i] // newest first
		if !v.Vector.Equal(p.vv) {
			t.Fatalf("version %d vector %v, want %v", i, v.Vector, p.vv)
		}
		md, err := e.svc.GetTextAt(ctx, alice, id, v.Vector)
		if err != nil || md != p.md {
			t.Fatalf("text at version %d: %q (%v), want %q", i, md, err, p.md)
		}
	}
	if !slices.Equal(versions[0].Authors, []string{bob.UserID}) || !slices.Equal(versions[len(versions)-1].Authors, []string{alice.UserID}) {
		t.Fatalf("authors %v / %v", versions[0].Authors, versions[len(versions)-1].Authors)
	}
	for i := range points {
		for j := range points {
			hunks, err := e.svc.Diff(ctx, alice, id, points[i].vv, points[j].vv)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := dp.ApplyTextPatch(points[i].md, hunks); err != nil || got != points[j].md {
				t.Fatalf("diff %d->%d does not apply: %v", i, j, err)
			}
		}
	}
	page1, token, _ := e.svc.ListVersions(ctx, alice, id, 2, "")
	page2, _, _ := e.svc.ListVersions(ctx, alice, id, 2, token)
	if len(page1) != 2 || len(page2) != 2 || page2[0].LastSeq >= page1[1].LastSeq {
		t.Fatalf("pages %v %v", page1, page2)
	}
	future := points[len(points)-1].vv.Clone()
	future[1<<40] += 10
	_, err = e.svc.GetTextAt(ctx, alice, id, future)
	mustErr(t, err, domain.ErrOutOfRange)
	inconsistent := crdt.VersionVector{1 << 40: 3}
	_, err = e.svc.GetTextAt(ctx, alice, id, inconsistent)
	mustErr(t, err, domain.ErrInvalidArgument)
}

func TestBlocksChangedIsDebouncedAndCarriesOnlyChanges(t *testing.T) {
	e := newEnv(t, Config{Debounce: 2 * time.Second})
	id := e.newNote(t, "first\n\nsecond")
	c := e.client(t, id, bob, 1<<40)
	if err := c.connect(); err != nil {
		t.Fatal(err)
	}
	c.pump()
	before := len(e.store.Outbox())
	for _, ch := range "abc" {
		c.submit(c.insert(c.seq.Len(), string(ch)))
	}
	flush := func() {
		e.state(t, id, func(st *noteState) {
			if err := st.flushProjection(ctx, false); err != nil {
				t.Fatal(err)
			}
		})
	}
	flush()
	if n := len(e.store.Outbox()); n != before {
		t.Fatalf("published before the debounce interval: %d events", n-before)
	}
	e.clock.Advance(3 * time.Second)
	flush()
	events := e.store.Outbox()[before:]
	if len(events) != 1 || events[0].Kind != domain.EventBlocksChanged || len(events[0].Blocks) != 1 || events[0].Blocks[0].Text != "secondabc" {
		t.Fatalf("events %+v", events)
	}
}

func TestPurgeErasesTheNoteAndKeepsItNotFound(t *testing.T) {
	e := newEnv(t, Config{})
	id := e.newNote(t, "secret")
	if err := e.svc.DeleteNote(ctx, alice, id); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(31 * 24 * time.Hour)
	n, err := e.svc.PurgeTrashed(ctx, e.clock.Now().Add(-30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("purged %d, %v", n, err)
	}
	_, _, _, err = e.svc.GetNote(ctx, alice, id, true)
	mustErr(t, err, domain.ErrNotFound)
	mustErr(t, e.svc.RestoreNote(ctx, alice, id), domain.ErrNotFound)
	if e.store.Batches(id) != nil {
		t.Fatal("the op log must be erased")
	}
	if last := e.store.Outbox(); last[len(last)-1].Kind != domain.EventNotePurged {
		t.Fatalf("last event %v", last[len(last)-1].Kind)
	}
}

func TestIdleActorsAreEvictedAndReloadOnDemand(t *testing.T) {
	e := newEnv(t, Config{Tick: 2 * time.Millisecond, IdleTimeout: time.Minute})
	id := e.newNote(t, "kept")
	if got := e.text(t, id); got != "\u001ekept" {
		t.Fatal(got)
	}
	e.clock.Advance(2 * time.Minute)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, loaded := e.svc.reg.loaded(id); !loaded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the idle actor was not evicted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := e.text(t, id); got != "\u001ekept" {
		t.Fatalf("reloaded %q", got)
	}
}

func TestPresenceReachesOthersAndNewcomers(t *testing.T) {
	e := newEnv(t, Config{})
	id := e.newNote(t, "")
	a := e.client(t, id, alice, 1<<40)
	b := e.client(t, id, bob, 1<<41)
	if err := a.connect(); err != nil {
		t.Fatal(err)
	}
	if err := b.connect(); err != nil {
		t.Fatal(err)
	}
	if err := a.sess.SetPresence(ctx, "1:1", "1:2"); err != nil {
		t.Fatal(err)
	}
	f := <-b.sess.Frames()
	if f.Kind != FramePresence || f.Presence.UserID != alice.UserID || f.Presence.Replica != 1<<40 || f.Presence.Head != "1:2" {
		t.Fatalf("frame %+v", f)
	}
	select {
	case f := <-a.sess.Frames():
		t.Fatalf("presence echoed to its sender: %+v", f)
	default:
	}
	late := e.client(t, id, carol, 1<<42)
	if err := late.connect(); err != nil {
		t.Fatal(err)
	}
	if f := <-late.sess.Frames(); f.Kind != FramePresence || f.Presence.Anchor != "1:1" {
		t.Fatalf("newcomer got %+v", f)
	}
	mustErr(t, a.sess.SetPresence(ctx, strings.Repeat("9", 100), ""), domain.ErrInvalidArgument)
}

func TestCreateNoteImportsMarkdownBlocks(t *testing.T) {
	e := newEnv(t, Config{})
	md := "# Plan\n\n- [ ] write\n- [x] test\n\n```go\nfunc main() {}\n```"
	id := e.newNote(t, md)
	var bs []blocks.Block
	e.state(t, id, func(st *noteState) { bs, _ = st.blocks() })
	if len(bs) != 4 || bs[0].Type != blocks.Heading1 || !bs[2].Checked || bs[3].Language != "go" {
		t.Fatalf("blocks %+v", bs)
	}
	if _, got, _, _ := e.svc.GetNote(ctx, alice, id, true); got != md {
		t.Fatalf("markdown %q", got)
	}
}

// Identity learns of notes from documents.events, a moment after they are
// created; CreateNote registers the note with identity at once so its
// creator can open it immediately (found end to end: GET and sync of a
// just-created note were NOT_FOUND).
func TestCreatorCanReadANewNoteBeforeIdentityHearsTheEvent(t *testing.T) {
	e := newEnv(t, Config{})
	e.auth.AllowCreate(workspace, alice.UserID)
	n, err := e.svc.CreateNote(context.Background(), alice, workspace, "Fresh", "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.auth.Registrations(); len(got) != 1 || got[0] != string(n.ID) {
		t.Fatalf("registrations %v, want the new note", got)
	}
	if _, md, _, err := e.svc.GetNote(context.Background(), alice, n.ID, true); err != nil || md == "" {
		t.Fatalf("the creator cannot read the note just created: %v", err)
	}
}

// A failed registration is not a failed create: the note is committed and
// identity catches up from NoteCreated.
func TestCreateNoteSucceedsWhenRegistrationFails(t *testing.T) {
	e := newEnv(t, Config{})
	registry := failingRegistry{}
	e.svc.registry = registry
	e.auth.AllowCreate(workspace, alice.UserID)
	n, err := e.svc.CreateNote(context.Background(), alice, workspace, "Fresh", "Hello")
	if err != nil {
		t.Fatalf("create failed with the registry down: %v", err)
	}
	if _, err := e.store.GetNote(context.Background(), n.ID); err != nil {
		t.Fatalf("note not stored: %v", err)
	}
}

type failingRegistry struct{}

func (failingRegistry) RegisterNote(context.Context, domain.Subject, domain.NoteID, domain.WorkspaceID) error {
	return errors.New("identity unavailable")
}
