package trigram

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

func TestFoldUsesTheSmallestMemberOfTheCaseOrbit(t *testing.T) {
	cases := map[rune]rune{
		'a': 'A', 'A': 'A', 'k': 'K', 'K': 'K', // Kelvin sign
		'ſ': 'S', 's': 'S', // long s
		'σ': 'Σ', 'ς': 'Σ', 'Σ': 'Σ',
		'é': 'É', '1': '1', '🙂': '🙂', '�': '�',
	}
	for in, want := range cases {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTrigramsAreFoldedDistinctAndCountCodePoints(t *testing.T) {
	if !slices.Equal(Trigrams("aBc"), Trigrams("AbC")) {
		t.Fatal("case changed the trigrams")
	}
	if got := Trigrams("é🙂x"); len(got) != 1 || got[0].String() != "É🙂X" {
		t.Fatalf("Trigrams(é🙂x) = %v", got)
	}
	if got := Trigrams("aaaa"); len(got) != 1 {
		t.Fatalf("repeated trigram counted %d times", len(got))
	}
	if Trigrams("ab") != nil {
		t.Fatal("two code points produced a trigram")
	}
}

func TestIndexInvariantsHoldAfterEveryAddUpdateAndRemove(t *testing.T) {
	r := newRand(testSeed(t))
	ix := NewIndex()
	present := map[DocID]bool{}
	for step := range 1500 {
		id := DocID(r.IntN(40))
		doc := randomDoc(r, 30)
		switch op := r.IntN(3); {
		case op == 0 && !present[id]:
			if err := ix.Add(id, doc); err != nil {
				t.Fatal(err)
			}
			present[id] = true
		case op == 0:
			if err := ix.Add(id, doc); !errors.Is(err, ErrDuplicateDoc) {
				t.Fatalf("duplicate Add returned %v", err)
			}
		case op == 1:
			err := ix.Update(id, doc)
			if present[id] != (err == nil) || err != nil && !errors.Is(err, ErrUnknownDoc) {
				t.Fatalf("Update with present=%v returned %v", present[id], err)
			}
		default:
			err := ix.Remove(id)
			if present[id] != (err == nil) || err != nil && !errors.Is(err, ErrUnknownDoc) {
				t.Fatalf("Remove with present=%v returned %v", present[id], err)
			}
			delete(present, id)
		}
		if err := ix.CheckInvariants(); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}
}

func TestIndexCheckInvariantsDetectsCorruption(t *testing.T) {
	ix := mustIndex(t, map[DocID]Doc{1: {Text: "abcd"}, 2: {Text: "bcde", Code: true, Language: "Go"}})
	ix.postings[MakeTrigram('a', 'b', 'c')].Add(2)
	if err := ix.CheckInvariants(); !errors.Is(err, ErrInvariant) {
		t.Fatalf("stale posting not detected: %v", err)
	}
	ix = mustIndex(t, map[DocID]Doc{2: {Text: "bcde", Code: true, Language: "Go"}})
	ix.languages["go"].Remove(2)
	if err := ix.CheckInvariants(); !errors.Is(err, ErrInvariant) {
		t.Fatalf("missing language entry not detected: %v", err)
	}
}

func TestSnapshotRoundTripsAndIsDeterministic(t *testing.T) {
	r := newRand(testSeed(t))
	for range 30 {
		docs := map[DocID]Doc{}
		for range r.IntN(30) {
			docs[DocID(r.IntN(1<<20))] = randomDoc(r, 40)
		}
		ix := mustIndex(t, docs)
		data, err := ix.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		back, err := UnmarshalIndex(data)
		if err != nil {
			t.Fatal(err)
		}
		if err := back.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		again, _ := back.MarshalBinary()
		if !bytes.Equal(again, data) || back.Len() != ix.Len() || back.TrigramCount() != ix.TrigramCount() {
			t.Fatal("snapshot did not round-trip")
		}
	}
}

func TestSnapshotDecodingRejectsEveryTruncation(t *testing.T) {
	ix := mustIndex(t, map[DocID]Doc{1: {Text: "abcdef"}, 7: {Text: "fn main()", Code: true, Language: "rust"}})
	data, _ := ix.MarshalBinary()
	for n := range len(data) {
		if _, err := UnmarshalIndex(data[:n]); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("prefix of %d bytes: %v", n, err)
		}
	}
	if _, err := UnmarshalIndex(append(slices.Clone(data), 0)); !errors.Is(err, ErrCorrupt) {
		t.Fatal("trailing byte accepted")
	}
}

func FuzzUnmarshalIndex(f *testing.F) {
	ix := NewIndex()
	_ = ix.Add(1, Doc{Text: "abcdef"})
	_ = ix.Add(3, Doc{Text: "Kelvin K", Code: true, Language: "go"})
	data, _ := ix.MarshalBinary()
	f.Add(data)
	f.Fuzz(func(t *testing.T, data []byte) {
		ix, err := UnmarshalIndex(data)
		if err != nil {
			return
		}
		// Structure was checked; posting contents may still disagree with the
		// texts, which CheckInvariants reports without panicking.
		_ = ix.CheckInvariants()
		if again, _ := ix.MarshalBinary(); !bytes.Equal(again, data) {
			t.Fatal("accepted a non-canonical snapshot")
		}
	})
}
