package domain

import (
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/libs/dp"
)

func testSeed(t testing.TB) uint64 {
	t.Helper()
	if v := os.Getenv("SKEIN_SEED"); v != "" {
		seed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return seed
	}
	return rand.Uint64()
}

func newRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)) }

var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

func randomNote(r *rand.Rand) string {
	words := []string{"raft", "log", "snapshot", "漢字", "🙂", "follower", "# Heading", "- item", ""}
	var lines []string
	for range r.IntN(12) {
		var w []string
		for range r.IntN(6) {
			w = append(w, words[r.IntN(len(words))])
		}
		lines = append(lines, strings.Join(w, " "))
	}
	return strings.Join(lines, "\n")
}

// mutate applies a few character edits, mostly confined to one line.
func mutate(r *rand.Rand, s string) string {
	rs := []rune(s)
	for range 1 + r.IntN(4) {
		i := r.IntN(len(rs) + 1)
		switch r.IntN(3) {
		case 0:
			rs = slices.Insert(rs, i, []rune("xé\n")[r.IntN(3)])
		case 1:
			if i < len(rs) {
				rs = slices.Delete(rs, i, i+1)
			}
		default:
			if i < len(rs) {
				rs[i] = 'Z'
			}
		}
	}
	return string(rs)
}

func checkDiff(t *testing.T, a, b string, hunks []dp.TextHunk) {
	t.Helper()
	got, err := dp.ApplyTextPatch(a, hunks)
	if err != nil || got != b {
		t.Fatalf("patch of %q gives %q (%v), want %q", a, got, err, b)
	}
	inverse := make([]dp.TextHunk, len(hunks))
	for i, h := range hunks {
		inverse[i] = h
		switch h.Kind {
		case dp.HunkInsert:
			inverse[i].Kind = dp.HunkDelete
		case dp.HunkDelete:
			inverse[i].Kind = dp.HunkInsert
		}
	}
	if back, err := dp.ApplyTextPatch(b, canonicalHunks(inverse)); err != nil || back != a {
		t.Fatalf("inverse patch fails: %v", err)
	}
	for i, h := range hunks {
		if h.Text == "" || (i > 0 && hunks[i-1].Kind == h.Kind) || (i > 0 && hunks[i-1].Kind == dp.HunkInsert && h.Kind == dp.HunkDelete) {
			t.Fatalf("hunks not canonical at %d: %+v", i, hunks)
		}
	}
}

func TestDiffTextAppliesAndIsCanonical(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	for i := 0; i < 3000; i++ {
		a := randomNote(r)
		b := mutate(r, a)
		if r.IntN(5) == 0 {
			b = randomNote(r)
		}
		checkDiff(t, a, b, DiffText(a, b))
	}
}

// An edit inside one line costs exactly what a full character diff (the
// shortest edit script) costs: refining by line loses nothing there.
func TestDiffTextIsMinimalForEditsWithinOneLine(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	for i := 0; i < 2000; i++ {
		lines := strings.Split(randomNote(r)+"\nlast line here", "\n")
		k := r.IntN(len(lines))
		before := strings.Join(lines, "\n")
		rs := []rune(lines[k])
		j := r.IntN(len(rs) + 1)
		lines[k] = string(rs[:j]) + "ins" + string(rs[min(j+1, len(rs)):])
		after := strings.Join(lines, "\n")
		ours := DiffText(before, after)
		checkDiff(t, before, after, ours)
		var d, best int
		for _, h := range ours {
			if h.Kind != dp.HunkEqual {
				d += len([]rune(h.Text))
			}
		}
		for _, h := range dp.DiffChars(before, after) {
			if h.Kind != dp.HunkEqual {
				best += len([]rune(h.Text))
			}
		}
		if d != best {
			t.Fatalf("%q -> %q: %d edits, shortest is %d", before, after, d, best)
		}
	}
}

func TestDiffTextExampleFromTheAPIDocs(t *testing.T) {
	a := "# Raft log compaction\nSnapshots bound the log."
	b := "# Raft log compaction\nSnapshots bound the log and let slow followers catch up."
	want := []dp.TextHunk{
		{Kind: dp.HunkEqual, Text: "# Raft log compaction\nSnapshots bound the log"},
		{Kind: dp.HunkInsert, Text: " and let slow followers catch up"},
		{Kind: dp.HunkEqual, Text: "."},
	}
	if got := DiffText(a, b); !slices.Equal(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func edit(seq int64, replica crdt.ReplicaID, at time.Duration) Batch {
	return Batch{Seq: seq, Replica: replica, For: replica, Kind: KindEdit, At: t0.Add(at), UserID: "u" + strconv.Itoa(int(replica))}
}

func TestUndoStacksGroupEditsByTimeAndReplica(t *testing.T) {
	bs := []Batch{
		edit(1, 10, 0), edit(2, 10, time.Second), // one group
		edit(3, 20, 2*time.Second),  // another replica
		edit(4, 10, 10*time.Second), // after the window: new group
		{Seq: 5, Replica: 77, For: 10, Kind: KindSuggestion, Group: 5, At: t0.Add(11 * time.Second)},
		{Seq: 6, Replica: 77, For: 10, Kind: KindSuggestion, Group: 5, At: t0.Add(11 * time.Second)},
		{Seq: 7, Replica: 77, For: 10, Kind: KindSuggestion, Group: 7, At: t0.Add(12 * time.Second)},
	}
	st := UndoStacks(bs, 10, 2*time.Second)
	want := []ChangeGroup{{Batches: []int64{1, 2}}, {Batches: []int64{4}}, {Batches: []int64{5, 6}}, {Batches: []int64{7}}}
	if !slices.EqualFunc(st.Undo, want, func(a, b ChangeGroup) bool { return slices.Equal(a.Batches, b.Batches) }) {
		t.Fatalf("undo stack %+v", st.Undo)
	}
	if other := UndoStacks(bs, 20, 2*time.Second); len(other.Undo) != 1 || other.Undo[0].ID() != 3 {
		t.Fatalf("replica 20: %+v", other.Undo)
	}
}

func TestUndoRedoAndANewEditMoveGroupsBetweenStacks(t *testing.T) {
	bs := []Batch{
		edit(1, 10, 0),
		edit(2, 10, time.Minute),
		{Seq: 3, Replica: ServerReplica, For: 10, Kind: KindUndo, Ref: 2},
		{Seq: 4, Replica: ServerReplica, For: 10, Kind: KindUndo, Ref: 1},
		{Seq: 5, Replica: ServerReplica, For: 10, Kind: KindRedo, Ref: 4},
	}
	st := UndoStacks(bs, 10, time.Second)
	if len(st.Undo) != 1 || st.Undo[0].ID() != 5 || !slices.Equal(st.Redo, []int64{3}) {
		t.Fatalf("after redo: %+v", st)
	}
	st = UndoStacks(append(bs, edit(6, 10, 2*time.Minute)), 10, time.Second)
	if len(st.Redo) != 0 || len(st.Undo) != 2 {
		t.Fatalf("a new edit must clear redo: %+v", st)
	}
}

func TestGroupVersionsCutsAtTimeGapsAndKeepsCumulativeVectors(t *testing.T) {
	bs := []Batch{
		{Seq: 1, Replica: 10, First: 1, Last: 5, UserID: "a", At: t0},
		{Seq: 2, Replica: 20, First: 1, Last: 2, UserID: "b", At: t0.Add(time.Minute)},
		{Seq: 3, Replica: 10, First: 6, Last: 9, UserID: "a", At: t0.Add(time.Hour)},
	}
	vs := GroupVersions(bs, 5*time.Minute)
	if len(vs) != 2 {
		t.Fatalf("%d versions", len(vs))
	}
	if !slices.Equal(vs[0].Authors, []string{"a", "b"}) || vs[0].Vector[10] != 5 || vs[0].Vector[20] != 2 || vs[0].LastSeq != 2 {
		t.Fatalf("first version %+v", vs[0])
	}
	if vs[1].Vector[10] != 9 || vs[1].Vector[20] != 2 || !slices.Equal(vs[1].Authors, []string{"a"}) {
		t.Fatalf("second version %+v", vs[1])
	}
}

// docWith returns a server-replica document whose text was typed by the
// given replicas in turn, with the batches that recorded it.
type history struct {
	doc     *blocks.Document
	peers   map[crdt.ReplicaID]*crdt.Sequence
	batches []Batch
}

func newHistory(t *testing.T) *history {
	d, err := blocks.NewDocument(ServerReplica)
	if err != nil {
		t.Fatal(err)
	}
	return &history{doc: d, peers: map[crdt.ReplicaID]*crdt.Sequence{}}
}

func (h *history) peer(t *testing.T, r crdt.ReplicaID) *crdt.Sequence {
	if p, ok := h.peers[r]; ok {
		return p
	}
	p, err := crdt.New(r)
	if err != nil {
		t.Fatal(err)
	}
	h.peers[r] = p
	return p
}

// sync brings a peer up to date with the document.
func (h *history) sync(t *testing.T, r crdt.ReplicaID) *crdt.Sequence {
	p := h.peer(t, r)
	for _, op := range h.doc.Sequence().OpsSince(p.Version()) {
		if err := p.Apply(op); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// record applies ops to the document as one batch.
func (h *history) record(t *testing.T, ops []crdt.Op, b Batch) Batch {
	t.Helper()
	for _, op := range ops {
		if err := h.doc.Sequence().Apply(op); err != nil {
			t.Fatal(err)
		}
	}
	b.Seq = int64(len(h.batches) + 1)
	b.Ops = ops
	if len(ops) > 0 {
		b.Replica, b.First, b.Last = ops[0].ID.Replica, ops[0].ID.Counter, ops[len(ops)-1].ID.Counter
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	h.batches = append(h.batches, b)
	return b
}

func (h *history) typeAt(t *testing.T, r crdt.ReplicaID, index int, text string) Batch {
	ops, err := h.sync(t, r).Insert(index, text)
	if err != nil {
		t.Fatal(err)
	}
	return h.record(t, ops, Batch{For: r, Kind: KindEdit, UserID: "user" + strconv.Itoa(int(r))})
}

func (h *history) deleteAt(t *testing.T, r crdt.ReplicaID, index, count int) Batch {
	ops, err := h.sync(t, r).Delete(index, count)
	if err != nil {
		t.Fatal(err)
	}
	return h.record(t, ops, Batch{For: r, Kind: KindEdit, UserID: "user" + strconv.Itoa(int(r))})
}

func (h *history) invert(t *testing.T, of []crdt.Op, kind BatchKind, ref int64, r crdt.ReplicaID) Batch {
	t.Helper()
	before := h.doc.Sequence().Version()
	ops, _, err := Inverse(h.doc, of)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.doc.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	// Inverse applied them already; record without re-applying.
	b := Batch{Seq: int64(len(h.batches) + 1), For: r, Kind: kind, Ref: ref, Ops: ops, Replica: ServerReplica}
	if len(ops) > 0 {
		b.First, b.Last = before.Get(ServerReplica)+1, ops[len(ops)-1].ID.Counter
	}
	h.batches = append(h.batches, b)
	return b
}

func TestInverseUndoesOnlyTheGroupsOwnEdits(t *testing.T) {
	h := newHistory(t)
	h.typeAt(t, 10, 0, "\u001eHello world")
	b2 := h.deleteAt(t, 10, 6, 6) // "\u001eHello"
	h.typeAt(t, 20, 6, " there")  // someone else
	undo := h.invert(t, b2.Ops, KindUndo, b2.Seq, 10)
	if got := h.doc.Sequence().Text(); got != "\u001eHello there world" && got != "\u001eHello world there" {
		t.Fatalf("after undoing the delete: %q", got)
	}
	if !strings.Contains(h.doc.Sequence().Text(), " there") {
		t.Fatal("the other replica's text must survive")
	}
	h.invert(t, undo.Ops, KindRedo, undo.Seq, 10)
	if got := h.doc.Sequence().Text(); got != "\u001eHello there" {
		t.Fatalf("after redo: %q", got)
	}
}

func TestInverseOfAnInsertLeavesTextOthersDeletedAlone(t *testing.T) {
	h := newHistory(t)
	b1 := h.typeAt(t, 10, 0, "\u001eabc")
	h.deleteAt(t, 20, 2, 1) // replica 20 deletes "b"
	h.invert(t, b1.Ops, KindUndo, b1.Seq, 10)
	if got := h.doc.Sequence().Text(); got != "" {
		t.Fatalf("undo of the typing must delete what is left: %q", got)
	}
}

func TestInverseCopiesBlockAttributesToReinsertedMarkers(t *testing.T) {
	h := newHistory(t)
	b1 := h.typeAt(t, 10, 0, "\u001eTitle")
	marker := b1.Ops[0].ID
	if _, err := h.doc.Attributes().Apply(blocks.AttrWrite{Block: marker, Key: blocks.AttrType, Lamport: 99, Replica: 10, Type: blocks.Heading1}); err != nil {
		t.Fatal(err)
	}
	b2 := h.deleteAt(t, 10, 0, 6)
	h.invert(t, b2.Ops, KindUndo, b2.Seq, 10)
	bs, err := h.doc.Blocks()
	if err != nil || len(bs) != 1 || bs[0].Type != blocks.Heading1 || bs[0].Text != "Title" || bs[0].ID == marker {
		t.Fatalf("got %+v %v: a copy of the heading, with a new marker", bs, err)
	}
}

func TestBlameAttributesMarkdownPositionsToBatches(t *testing.T) {
	h := newHistory(t)
	h.typeAt(t, 10, 0, "\u001eRaft\u001eSnapshots bound the log.")
	h.typeAt(t, 77, 29, " and let slow followers catch up")
	h.batches[1].UserID, h.batches[1].AgentRunID = "user10", "run-1"
	_, _ = h.doc.SetAttrs(h.batches[0].Ops[0].ID, blocks.Attrs{Type: blocks.Heading1})
	bs, err := h.doc.Blocks()
	if err != nil {
		t.Fatal(err)
	}
	spans := Blame(bs, NewAuthorship(h.batches))
	md := blocks.Export(bs)
	if md != "# Raft\n\nSnapshots bound the log and let slow followers catch up." {
		t.Fatalf("markdown %q", md)
	}
	want := []BlameSpan{
		{Start: 0, End: 31, UserID: "user10"},
		{Start: 31, End: 63, UserID: "user10", AgentRunID: "run-1"},
		{Start: 63, End: 64, UserID: "user10"},
	}
	if len(spans) != len(want) {
		t.Fatalf("spans %+v", spans)
	}
	for i := range want {
		if spans[i].Start != want[i].Start || spans[i].End != want[i].End || spans[i].UserID != want[i].UserID || spans[i].AgentRunID != want[i].AgentRunID {
			t.Fatalf("span %d: %+v, want %+v", i, spans[i], want[i])
		}
	}
}

func TestNextProjectionReportsOnlyDifferences(t *testing.T) {
	note := Note{ID: "n", WorkspaceID: "w"}
	b := func(id uint64, typ blocks.Type, text string) blocks.Block {
		return blocks.Block{ID: crdt.ID{Replica: 9, Counter: id}, Attrs: blocks.Attrs{Type: typ}, Text: text}
	}
	p1, ev := NextProjection(note, Projection{}, []blocks.Block{b(1, blocks.Paragraph, "see [[A]]"), b(2, blocks.Code, "x")}, 3, Subject{}, t0)
	if len(ev) != 2 || ev[0].Kind != EventBlocksChanged || len(ev[0].Blocks) != 2 || ev[1].Kind != EventLinksChanged || !slices.Equal(ev[1].Links, []string{"A"}) {
		t.Fatalf("first publication %+v", ev)
	}
	_, ev = NextProjection(note, p1, []blocks.Block{b(1, blocks.Paragraph, "see [[A]]"), b(3, blocks.Paragraph, "new")}, 4, Subject{}, t0)
	if len(ev) != 1 || len(ev[0].Blocks) != 1 || ev[0].Blocks[0].BlockID != "9:3" || !slices.Equal(ev[0].RemovedBlockIDs, []string{"9:2"}) {
		t.Fatalf("second publication %+v", ev)
	}
	_, ev = NextProjection(note, p1, []blocks.Block{b(1, blocks.Paragraph, "see [[A]]"), b(2, blocks.Code, "x")}, 5, Subject{}, t0)
	if len(ev) != 0 {
		t.Fatalf("no change must publish nothing: %+v", ev)
	}
}

func TestNoteLifecycle(t *testing.T) {
	n := Note{Title: "a"}
	if changed := n.Trash(t0); !changed || n.Trash(t0) || !n.Trashed() {
		t.Fatal("trash then trash again")
	}
	if _, err := n.Rename("b", t0); err == nil {
		t.Fatal("a trashed note cannot be renamed")
	}
	if !n.Restore(t0) || n.Restore(t0) {
		t.Fatal("restore then restore again")
	}
	for _, bad := range []string{"", "   ", "a\nb", strings.Repeat("x", MaxTitleRunes+1)} {
		if _, err := NormalizeTitle(bad); err == nil {
			t.Errorf("title %q accepted", bad)
		}
	}
	if got, _ := NormalizeTitle("  Raft  "); got != "Raft" {
		t.Errorf("got %q", got)
	}
}

func TestSplitRunsCutsAtReplicaChanges(t *testing.T) {
	op := func(r crdt.ReplicaID, c uint64) crdt.Op {
		return crdt.Op{ID: crdt.ID{Replica: r, Counter: c}, Kind: crdt.OpDelete, Target: crdt.ID{Replica: 1, Counter: 1}}
	}
	senders := map[crdt.ReplicaID]Sender{10: {Replica: 10, UserID: "a", Kind: KindEdit}, 20: {Replica: 20, UserID: "b", Kind: KindSuggestion}}
	got := SplitRuns([]crdt.Op{op(10, 1), op(10, 2), op(20, 1), op(10, 3)}, senders, t0)
	if len(got) != 3 || got[0].Last != 2 || got[1].UserID != "b" || got[1].Kind != KindSuggestion || got[2].First != 3 {
		t.Fatalf("got %+v", got)
	}
	for _, b := range got {
		if err := b.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
