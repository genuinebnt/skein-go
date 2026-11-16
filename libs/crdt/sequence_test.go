package crdt

import (
	"errors"
	"slices"
	"testing"
)

func TestNewRejectsReservedReplica(t *testing.T) {
	if _, err := New(0); !errors.Is(err, ErrInvalidReplica) {
		t.Fatalf("New(0) error = %v, want ErrInvalidReplica", err)
	}
}

func TestEmptySequenceIsValid(t *testing.T) {
	s := newSeq(t, 1)
	mustValid(t, s)
	if s.Len() != 0 || s.Text() != "" {
		t.Fatalf("empty sequence has text %q", s.Text())
	}
}

func TestLocalInsertAndDelete(t *testing.T) {
	s := newSeq(t, 1)
	mustInsert(t, s, 0, "helo")
	mustInsert(t, s, 3, "l")
	mustInsert(t, s, 5, " world")
	mustInsert(t, s, 0, ">")
	if got := s.Text(); got != ">hello world" {
		t.Fatalf("text = %q", got)
	}
	mustDelete(t, s, 0, 1)
	mustDelete(t, s, 5, 6)
	if got := s.Text(); got != "hello" {
		t.Fatalf("after deletes text = %q", got)
	}
}

func TestIndexesCountCodePointsNotBytes(t *testing.T) {
	s := newSeq(t, 1)
	mustInsert(t, s, 0, "a🙂b")
	if s.Len() != 3 {
		t.Fatalf("Len() = %d, want 3 code points", s.Len())
	}
	mustInsert(t, s, 2, "漢")
	if got := s.Text(); got != "a🙂漢b" {
		t.Fatalf("text = %q", got)
	}
}

func TestLocalEditsRejectOutOfRange(t *testing.T) {
	s := newSeq(t, 1)
	mustInsert(t, s, 0, "abc")
	cases := []struct {
		name string
		err  error
	}{
		{"insert past end", func() error { _, err := s.Insert(4, "x"); return err }()},
		{"insert negative", func() error { _, err := s.Insert(-1, "x"); return err }()},
		{"delete past end", func() error { _, err := s.Delete(2, 2); return err }()},
		{"delete negative count", func() error { _, err := s.Delete(0, -1); return err }()},
	}
	for _, c := range cases {
		if !errors.Is(c.err, ErrIndexOutOfRange) {
			t.Errorf("%s: error = %v, want ErrIndexOutOfRange", c.name, c.err)
		}
	}
	if _, err := s.Insert(0, string([]byte{0xff})); !errors.Is(err, ErrInvalidText) {
		t.Errorf("invalid UTF-8: error = %v, want ErrInvalidText", err)
	}
	mustValid(t, s)
	if s.Text() != "abc" {
		t.Fatalf("rejected edits changed the text to %q", s.Text())
	}
}

// The core behavioural invariant for one replica: local editing must behave
// exactly like editing a plain array of runes.
func TestLocalEditsMatchSliceModel(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	s := newSeq(t, 7)
	var model []rune
	for step := 0; step < 2000; step++ {
		if len(model) > 0 && r.IntN(3) == 0 {
			i := r.IntN(len(model))
			n := 1 + r.IntN(min(5, len(model)-i))
			mustDelete(t, s, i, n)
			model = slices.Delete(model, i, i+n)
		} else {
			i := r.IntN(len(model) + 1)
			text := randomText(r, 5)
			mustInsert(t, s, i, text)
			model = slices.Insert(model, i, []rune(text)...)
		}
		if s.Text() != string(model) {
			t.Fatalf("step %d: text %q, model %q", step, s.Text(), string(model))
		}
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	a, b := newSeq(t, 1), newSeq(t, 2)
	ops := mustInsert(t, a, 0, "abc")
	ops = append(ops, mustDelete(t, a, 1, 1)...)
	mustApply(t, b, ops)
	mustApply(t, b, ops) // redelivery
	if b.Text() != "ac" || !b.Version().Equal(a.Version()) {
		t.Fatalf("after redelivery text %q, version %v", b.Text(), b.Version())
	}
}

func TestApplyRejectsOutOfOrderWithoutChanges(t *testing.T) {
	a, b := newSeq(t, 1), newSeq(t, 2)
	ops := mustInsert(t, a, 0, "ab")
	if err := b.Apply(ops[1]); !errors.Is(err, ErrNotReady) {
		t.Fatalf("early op error = %v, want ErrNotReady", err)
	}
	mustValid(t, b)
	if b.Len() != 0 || b.Version().Total() != 0 {
		t.Fatalf("rejected op changed state")
	}
}

func TestApplyRejectsReusedID(t *testing.T) {
	a, b := newSeq(t, 1), newSeq(t, 2)
	ops := mustInsert(t, a, 0, "a")
	mustApply(t, b, ops)
	forged := ops[0]
	forged.Value = 'z'
	if err := b.Apply(forged); !errors.Is(err, ErrIDConflict) {
		t.Fatalf("forged op error = %v, want ErrIDConflict", err)
	}
	if b.Text() != "a" {
		t.Fatalf("forged op changed text to %q", b.Text())
	}
}

func TestValidateRejectsMalformedOps(t *testing.T) {
	good := Op{ID: ID{1, 2}, Lamport: 2, Kind: OpInsert, Parent: ID{1, 1}, Side: SideRight, Value: 'x'}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid op rejected: %v", err)
	}
	cases := map[string]func(op *Op){
		"replica zero":         func(op *Op) { op.ID.Replica = 0 },
		"counter zero":         func(op *Op) { op.ID.Counter = 0 },
		"unknown kind":         func(op *Op) { op.Kind = 9 },
		"unknown side":         func(op *Op) { op.Side = 5 },
		"surrogate value":      func(op *Op) { op.Value = 0xD800 },
		"own parent":           func(op *Op) { op.Parent = op.ID },
		"left child of root":   func(op *Op) { op.Parent, op.Side = Root, SideLeft },
		"parent from future":   func(op *Op) { op.Parent = ID{1, 9} },
		"insert sets target":   func(op *Op) { op.Target = ID{1, 1} },
		"delete of root":       func(op *Op) { *op = Op{ID: ID{1, 2}, Kind: OpDelete} },
		"delete of future":     func(op *Op) { *op = Op{ID: ID{1, 2}, Kind: OpDelete, Target: ID{1, 2}} },
		"delete sets a parent": func(op *Op) { *op = Op{ID: ID{1, 2}, Kind: OpDelete, Target: ID{1, 1}, Parent: ID{1, 1}} },
	}
	for name, mutate := range cases {
		op := good
		mutate(&op)
		if err := op.Validate(); !errors.Is(err, ErrInvalidOp) {
			t.Errorf("%s: error = %v, want ErrInvalidOp", name, err)
		}
	}
}

func TestCursorAnchorsSurviveRemoteEdits(t *testing.T) {
	a, b := newSeq(t, 1), newSeq(t, 2)
	mustApply(t, b, mustInsert(t, a, 0, "hello"))
	anchor, err := b.IDAt(4) // the 'o'
	if err != nil {
		t.Fatal(err)
	}
	mustApply(t, b, mustInsert(t, a, 0, ">> "))
	if i, visible, ok := b.IndexOf(anchor); !ok || !visible || i != 7 {
		t.Fatalf("IndexOf after remote insert = %d %v %v, want 7 true true", i, visible, ok)
	}
	mustApply(t, b, mustDelete(t, a, 7, 1))
	if i, visible, ok := b.IndexOf(anchor); !ok || visible || i != 7 {
		t.Fatalf("IndexOf after remote delete = %d %v %v, want 7 false true", i, visible, ok)
	}
}

func TestOpsSinceReturnsOnlyMissingOps(t *testing.T) {
	a := newSeq(t, 1)
	mustInsert(t, a, 0, "abc")
	v := a.Version()
	ops := mustInsert(t, a, 3, "de")
	got := a.OpsSince(v)
	if !slices.Equal(got, ops) {
		t.Fatalf("OpsSince = %v, want %v", got, ops)
	}
	if len(a.OpsSince(a.Version())) != 0 {
		t.Fatalf("OpsSince(own version) is not empty")
	}
}
