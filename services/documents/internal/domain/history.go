package domain

import (
	"slices"
	"sort"
	"time"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/libs/dp"
)

// ChangeGroup is one undoable unit: the batches that make it up, in order.
type ChangeGroup struct {
	Batches []int64
}

// ID names the group by its first batch, which an undo batch records in Ref.
func (g ChangeGroup) ID() int64 { return g.Batches[0] }

// UndoState is a replica's undo and redo stacks, top last. Redo holds the
// undo batches that can be redone.
type UndoState struct {
	Undo []ChangeGroup
	Redo []int64
}

// UndoStacks replays a note's batches (metadata is enough) and returns the
// undo and redo stacks of one replica. Edit batches of the replica join the
// top group while each arrives within window of the group's previous batch
// and nothing was undone in between; suggestion batches group by the
// ApplyOps call that wrote them. Imports and attribute loads are not
// anyone's to undo.
func UndoStacks(batches []Batch, replica crdt.ReplicaID, window time.Duration) UndoState {
	var st UndoState
	var lastAt time.Time
	open := false // the top group can still grow
	for _, b := range batches {
		if b.For != replica || replica == 0 {
			continue
		}
		switch b.Kind {
		case KindEdit:
			if open && len(st.Undo) > 0 && b.At.Sub(lastAt) <= window {
				top := &st.Undo[len(st.Undo)-1]
				top.Batches = append(top.Batches, b.Seq)
			} else {
				st.Undo = append(st.Undo, ChangeGroup{Batches: []int64{b.Seq}})
			}
			st.Redo, open, lastAt = nil, true, b.At
		case KindSuggestion:
			// Group is the first batch of the ApplyOps call; that batch has
			// Group == Seq, so it starts the group and later ones join it.
			if n := len(st.Undo); n > 0 && b.Group != 0 && b.Group != b.Seq && st.Undo[n-1].ID() == b.Group {
				st.Undo[n-1].Batches = append(st.Undo[n-1].Batches, b.Seq)
			} else {
				st.Undo = append(st.Undo, ChangeGroup{Batches: []int64{b.Seq}})
			}
			st.Redo, open = nil, false
		case KindUndo:
			if n := len(st.Undo); n > 0 && st.Undo[n-1].ID() == b.Ref {
				st.Undo = st.Undo[:n-1]
			}
			st.Redo, open = append(st.Redo, b.Seq), false
		case KindRedo:
			if n := len(st.Redo); n > 0 && st.Redo[n-1] == b.Ref {
				st.Redo = st.Redo[:n-1]
			}
			st.Undo, open = append(st.Undo, ChangeGroup{Batches: []int64{b.Seq}}), false
		}
	}
	return st
}

// Version is a point in a note's history: everything up to a version
// vector, with the people who wrote the batches grouped into it.
type Version struct {
	Vector  crdt.VersionVector
	Authors []string // user IDs in order of first appearance
	At      time.Time
	LastSeq int64
}

// GroupVersions groups batches into versions, oldest first: a batch joins
// the current version while it arrives within window of the previous batch.
// Each version's vector covers every batch up to its last one, which is a
// prefix of the causal log and so a consistent cut.
func GroupVersions(batches []Batch, window time.Duration) []Version {
	var out []Version
	vv := crdt.VersionVector{}
	var lastAt time.Time
	for i, b := range batches {
		if b.Last > vv[b.Replica] {
			vv[b.Replica] = b.Last
		}
		if i == 0 || b.At.Sub(lastAt) > window {
			out = append(out, Version{})
		}
		v := &out[len(out)-1]
		if b.UserID != "" && !slices.Contains(v.Authors, b.UserID) {
			v.Authors = append(v.Authors, b.UserID)
		}
		v.At, v.LastSeq, lastAt = b.At, b.Seq, b.At
		v.Vector = vv.Clone()
	}
	return out
}

// Authorship finds the batch that wrote an operation.
type Authorship struct {
	byReplica map[crdt.ReplicaID][]Batch // ascending by First
}

// NewAuthorship indexes batch metadata by replica and counter range.
func NewAuthorship(batches []Batch) *Authorship {
	a := &Authorship{byReplica: map[crdt.ReplicaID][]Batch{}}
	for _, b := range batches {
		if b.First != 0 {
			a.byReplica[b.Replica] = append(a.byReplica[b.Replica], b.Meta())
		}
	}
	for _, bs := range a.byReplica {
		slices.SortFunc(bs, func(x, y Batch) int {
			if x.First < y.First {
				return -1
			}
			if x.First > y.First {
				return 1
			}
			return 0
		})
	}
	return a
}

// Lookup returns the batch containing operation id.
func (a *Authorship) Lookup(id crdt.ID) (Batch, bool) {
	bs := a.byReplica[id.Replica]
	i := sort.Search(len(bs), func(i int) bool { return bs[i].Last >= id.Counter })
	if i < len(bs) && bs[i].First <= id.Counter {
		return bs[i], true
	}
	return Batch{}, false
}

// BlameSpan says who wrote [Start, End) of a note's Markdown, in code points.
type BlameSpan struct {
	Start, End int
	UserID     string
	AgentRunID string
	At         time.Time
}

// Blame attributes each code point of the note's Markdown export to the
// batch that inserted the element it came from. Markup Export adds (list
// markers, fences, separators) belongs to the block's marker, which is the
// edit that created the block. Consecutive code points by the same user and
// agent run form one span, timed by its latest edit.
func Blame(bs []blocks.Block, authors *Authorship) []BlameSpan {
	_, sources := blocks.ExportMapped(bs)
	var out []BlameSpan
	for pos, src := range sources {
		b := bs[src.Block]
		id := b.ID
		if src.Offset >= 0 {
			id = b.Chars[src.Offset]
		} else if id.IsRoot() && len(b.Chars) > 0 {
			id = b.Chars[0] // a leading block has no marker
		}
		batch, _ := authors.Lookup(id)
		if n := len(out); n > 0 && out[n-1].UserID == batch.UserID && out[n-1].AgentRunID == batch.AgentRunID {
			out[n-1].End = pos + 1
			if batch.At.After(out[n-1].At) {
				out[n-1].At = batch.At
			}
			continue
		}
		out = append(out, BlameSpan{Start: pos, End: pos + 1, UserID: batch.UserID, AgentRunID: batch.AgentRunID, At: batch.At})
	}
	return out
}

// DiffText diffs two versions of a note's Markdown by line first (patience,
// which keeps unique lines such as headings anchored), then by code point
// within each run of changed lines. A character diff of a whole note costs
// memory per code point (docs/PERF.md); refining only the changed lines
// keeps it proportional to the change. The hunks are canonical (no empty
// hunk, no two adjacent hunks of one kind, deletes before inserts) and
// concatenate to the old text (equal and delete) and the new (equal and
// insert).
func DiffText(a, b string) []dp.TextHunk {
	lines := dp.PatienceDiffLines(a, b)
	var out []dp.TextHunk
	emit := func(h dp.TextHunk) {
		if h.Text == "" {
			return
		}
		if n := len(out); n > 0 && out[n-1].Kind == h.Kind {
			out[n-1].Text += h.Text
			return
		}
		out = append(out, h)
	}
	for i := 0; i < len(lines); i++ {
		h := lines[i]
		if h.Kind == dp.HunkDelete && i+1 < len(lines) && lines[i+1].Kind == dp.HunkInsert {
			for _, c := range dp.DiffChars(h.Text, lines[i+1].Text) {
				emit(c)
			}
			i++
			continue
		}
		emit(h)
	}
	return canonicalHunks(out)
}

// canonicalHunks reorders an insert directly followed by a delete (which
// merging refined regions can produce) so deletes come first, and merges
// neighbours of one kind.
func canonicalHunks(hs []dp.TextHunk) []dp.TextHunk {
	for changed := true; changed; {
		changed = false
		for i := 0; i+1 < len(hs); i++ {
			if hs[i].Kind == dp.HunkInsert && hs[i+1].Kind == dp.HunkDelete {
				hs[i], hs[i+1] = hs[i+1], hs[i]
				changed = true
			}
		}
		var merged []dp.TextHunk
		for _, h := range hs {
			if n := len(merged); n > 0 && merged[n-1].Kind == h.Kind {
				merged[n-1].Text += h.Text
				continue
			}
			merged = append(merged, h)
		}
		if len(merged) != len(hs) {
			changed = true
		}
		hs = merged
	}
	return hs
}
