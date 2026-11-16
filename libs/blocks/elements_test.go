package blocks

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/genuinebnt/skein-go/libs/crdt"
)

// checkElementsAgainstSequence compares Elements with a slow oracle built
// from the sequence's own lookups: visible elements must be exactly IDAt(0),
// IDAt(1), ..., and each tombstone must sit where IndexOf says a cursor on it
// would land.
func checkElementsAgainstSequence(t testing.TB, s *crdt.Sequence) {
	t.Helper()
	elems, err := Elements(s.OpsSince(crdt.VersionVector{}))
	if err != nil {
		t.Fatal(err)
	}
	visible := 0
	for _, e := range elems {
		index, isVisible, ok := s.IndexOf(e.ID)
		if !ok || isVisible == e.Deleted || index != visible {
			t.Fatalf("element %s: IndexOf = %d %v %v, layout says index %d deleted %v", e.ID, index, isVisible, ok, visible, e.Deleted)
		}
		if !e.Deleted {
			id, err := s.IDAt(visible)
			if err != nil || id != e.ID {
				t.Fatalf("visible element %d: Elements says %s, IDAt says %s (%v)", visible, e.ID, id, err)
			}
			visible++
		}
	}
	if visible != s.Len() {
		t.Fatalf("%d visible elements, sequence has %d", visible, s.Len())
	}
}

func randomEdit(t testing.TB, r *rand.Rand, d *Document) {
	t.Helper()
	s := d.Sequence()
	alphabet := []rune("ab é漢🙂\u001e\u001e")
	if s.Len() > 0 && r.IntN(3) == 0 {
		i := r.IntN(s.Len())
		if _, err := s.Delete(i, 1+r.IntN(min(4, s.Len()-i))); err != nil {
			t.Fatal(err)
		}
		return
	}
	text := randomRunes(r, alphabet, 5)
	if text == "" {
		text = "x"
	}
	if _, err := s.Insert(r.IntN(s.Len()+1), text); err != nil {
		t.Fatal(err)
	}
}

func TestElementsMatchTheSequenceOrderUnderConcurrentEditing(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	for round := 0; round < 40; round++ {
		docs := []*Document{newDoc(t, 11), newDoc(t, 22), newDoc(t, 33)}
		for step := 0; step < 30; step++ {
			randomEdit(t, r, pick(r, docs))
			if r.IntN(4) == 0 {
				from, to := pick(r, docs), pick(r, docs)
				for _, op := range from.Sequence().OpsSince(to.Sequence().Version()) {
					if err := to.Sequence().Apply(op); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, d := range docs {
				checkElementsAgainstSequence(t, d.Sequence())
				mustValid(t, d)
			}
		}
	}
}

func TestEnterSplitsABlockAndBackspaceMergesIt(t *testing.T) {
	d := newDoc(t, 7)
	if _, _, err := d.AppendBlocks([]Block{{Attrs: Attrs{Type: Heading1}, Text: "Title"}, {Attrs: DefaultAttrs(), Text: "hello world"}}); err != nil {
		t.Fatal(err)
	}
	mustValid(t, d)
	// "\x1eTitle\x1ehello world": Enter after "hello" inserts a marker.
	if _, err := d.Sequence().Insert(12, string(Marker)); err != nil {
		t.Fatal(err)
	}
	mustValid(t, d)
	bs := mustBlocks(t, d)
	if len(bs) != 3 || bs[1].Text != "hello" || bs[2].Text != " world" || bs[2].Type != Paragraph {
		t.Fatalf("after split: %+v", contents(bs))
	}
	// Backspace at the start of the heading's following block deletes its
	// marker; its text joins the heading, which keeps its attributes.
	if _, err := d.Sequence().Delete(6, 1); err != nil {
		t.Fatal(err)
	}
	mustValid(t, d)
	bs = mustBlocks(t, d)
	if len(bs) != 2 || bs[0].Text != "Titlehello" || bs[0].Type != Heading1 {
		t.Fatalf("after merge: %+v", contents(bs))
	}
}

func TestTextBeforeTheFirstMarkerFormsALeadingBlock(t *testing.T) {
	d := newDoc(t, 7)
	if _, _, err := d.AppendBlocks([]Block{{Attrs: Attrs{Type: Quote}, Text: "q"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Sequence().Insert(0, "lead"); err != nil {
		t.Fatal(err)
	}
	mustValid(t, d)
	bs := mustBlocks(t, d)
	if len(bs) != 2 || !bs[0].ID.IsRoot() || bs[0].Text != "lead" || bs[0].Type != Paragraph || bs[1].Type != Quote {
		t.Fatalf("got %+v", bs)
	}
}

func TestAppendBlocksStampsAttributesWithTheSequenceClock(t *testing.T) {
	d := newDoc(t, 7)
	_, writes, err := d.AppendBlocks([]Block{
		{Attrs: Attrs{Type: Code, Language: "go"}, Text: "x := 1"},
		{Attrs: DefaultAttrs(), Text: "plain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(writes) != 2 {
		t.Fatalf("want a type and a language write, got %+v", writes)
	}
	for _, w := range writes {
		if w.Lamport != d.Sequence().Lamport() || w.Replica != 7 {
			t.Fatalf("write %+v not stamped with the clock %d", w, d.Sequence().Lamport())
		}
	}
	got := contents(mustBlocks(t, d))
	if got[0].Type != Code || got[0].Language != "go" || got[1].Type != Paragraph {
		t.Fatalf("got %+v", got)
	}
}

func TestSetAttrsBeatsEveryWriteSeen(t *testing.T) {
	d := newDoc(t, 7)
	_, _, _ = d.AppendBlocks([]Block{{Attrs: Attrs{Type: Heading1}, Text: "t"}})
	marker := mustBlocks(t, d)[0].ID
	// A remote write with a high timestamp arrives first.
	if _, err := d.Attributes().Apply(typeWrite(marker, 50, 99, Quote)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetAttrs(marker, Attrs{Type: Heading3}); err != nil {
		t.Fatal(err)
	}
	mustValid(t, d)
	if got := mustBlocks(t, d)[0].Type; got != Heading3 {
		t.Fatalf("type %s: a local change after seeing lamport 50 must win", got)
	}
}

func TestAppendBlocksRejectsMarkerInText(t *testing.T) {
	d := newDoc(t, 7)
	if _, _, err := d.AppendBlocks([]Block{{Attrs: DefaultAttrs(), Text: "a\u001eb"}}); !errors.Is(err, ErrMarkerInText) {
		t.Fatalf("err %v", err)
	}
	if d.Sequence().Len() != 0 {
		t.Fatal("nothing may be inserted when the input is rejected")
	}
}

func TestComposeIsTheInverseOfLayout(t *testing.T) {
	d := newDoc(t, 7)
	in := []Block{{Attrs: DefaultAttrs(), Text: "a"}, {Attrs: DefaultAttrs(), Text: ""}, {Attrs: DefaultAttrs(), Text: "漢🙂"}}
	_, _, _ = d.AppendBlocks(in)
	text, err := Compose(mustBlocks(t, d))
	if err != nil || text != d.Sequence().Text() || strings.Count(text, string(Marker)) != 3 {
		t.Fatalf("Compose = %q, %v; sequence %q", text, err, d.Sequence().Text())
	}
}

func TestElementsRejectsAnInsertBeforeItsParent(t *testing.T) {
	a := newDoc(t, 7).Sequence()
	ops, _ := a.Insert(0, "ab")
	if _, err := Elements([]crdt.Op{ops[1], ops[0]}); !errors.Is(err, crdt.ErrNotReady) {
		t.Fatalf("err %v, want ErrNotReady", err)
	}
}

// Past 0xD800 inserts the relabelling skips the surrogate range; a mistake
// there shifts every later element.
func TestElementsOrderPastTheSurrogateRange(t *testing.T) {
	d := newDoc(t, 7)
	s := d.Sequence()
	chunk := strings.Repeat("abcdefgh", 1000)
	for s.Len() < 0xD800+3000 {
		if _, err := s.Insert(s.Len()/2, chunk); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Delete(100, 5000); err != nil {
		t.Fatal(err)
	}
	elems, err := d.Elements()
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, e := range elems {
		if !e.Deleted {
			text.WriteRune(e.Value)
		}
	}
	if text.String() != s.Text() {
		t.Fatal("element values in layout order differ from the sequence text")
	}
	for _, i := range []int{0, 0xD7FF, 0xD800, 0xD801, s.Len() - 1} {
		id, _ := s.IDAt(i)
		visible := -1
		for _, e := range elems {
			if !e.Deleted {
				visible++
			}
			if visible == i {
				if e.ID != id {
					t.Fatalf("visible element %d: %s, sequence says %s", i, e.ID, id)
				}
				break
			}
		}
	}
}
