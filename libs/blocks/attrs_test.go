package blocks

import (
	"bytes"
	"encoding/hex"
	"errors"
	"slices"
	"testing"

	"github.com/genuinebnt/skein-go/libs/crdt"
)

func typeWrite(block crdt.ID, lamport uint64, replica crdt.ReplicaID, t Type) AttrWrite {
	return AttrWrite{Block: block, Key: AttrType, Lamport: lamport, Replica: replica, Type: t}
}

func mustApplyAttr(t testing.TB, a *Attributes, w AttrWrite) bool {
	t.Helper()
	changed, err := a.Apply(w)
	if err != nil {
		t.Fatalf("Apply(%+v): %v", w, err)
	}
	if err := a.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	return changed
}

func TestLaterLamportWinsWhateverTheArrivalOrder(t *testing.T) {
	block := crdt.ID{Replica: 7, Counter: 1}
	older := typeWrite(block, 5, 9, Heading1)
	newer := typeWrite(block, 6, 2, Code)
	for _, order := range [][]AttrWrite{{older, newer}, {newer, older}} {
		a := NewAttributes()
		for _, w := range order {
			mustApplyAttr(t, a, w)
		}
		if got := a.Get(block).Type; got != Code {
			t.Errorf("order %v: type %s, want code (lamport 6 beats 5 even from a smaller replica)", order, got)
		}
	}
}

func TestEqualLamportIsBrokenByReplicaThenValue(t *testing.T) {
	block := crdt.ID{Replica: 7, Counter: 1}
	a, b := typeWrite(block, 5, 3, Quote), typeWrite(block, 5, 4, Heading2)
	if !b.Wins(a) || a.Wins(b) {
		t.Fatal("replica 4 must beat replica 3 at equal lamport")
	}
	// The same stamp with two values only comes from a buggy client, but
	// replicas must still agree: the larger encoded value wins.
	x, y := typeWrite(block, 5, 3, Quote), typeWrite(block, 5, 3, Table)
	if !y.Wins(x) || x.Wins(y) {
		t.Fatal("table (11) must beat quote (8) at an equal stamp")
	}
}

func TestReapplyingAWriteChangesNothing(t *testing.T) {
	a := NewAttributes()
	w := typeWrite(crdt.ID{Replica: 7, Counter: 1}, 1, 7, Todo)
	if !mustApplyAttr(t, a, w) {
		t.Fatal("first apply must change the register")
	}
	if mustApplyAttr(t, a, w) || a.Len() != 1 {
		t.Fatalf("second apply changed the store (len %d)", a.Len())
	}
}

func TestRegistersOfOneBlockAreIndependent(t *testing.T) {
	block := crdt.ID{Replica: 7, Counter: 1}
	a := NewAttributes()
	mustApplyAttr(t, a, typeWrite(block, 10, 1, Bulleted))
	mustApplyAttr(t, a, AttrWrite{Block: block, Key: AttrIndent, Lamport: 3, Replica: 2, Indent: 2})
	got := a.Get(block)
	if got.Type != Bulleted || got.Indent != 2 {
		t.Fatalf("got %+v: a concurrent type change and indent change must both survive", got)
	}
}

func TestUnwrittenBlockHasDefaultAttributes(t *testing.T) {
	if got := NewAttributes().Get(crdt.ID{Replica: 1, Counter: 1}); got != DefaultAttrs() {
		t.Fatalf("got %+v", got)
	}
}

func TestValidateRejectsMalformedWrites(t *testing.T) {
	block := crdt.ID{Replica: 7, Counter: 1}
	cases := map[string]AttrWrite{
		"replica zero":         {Block: block, Key: AttrType, Lamport: 1, Type: Code},
		"lamport zero":         {Block: block, Key: AttrType, Replica: 1, Type: Code},
		"unknown key":          {Block: block, Key: 9, Lamport: 1, Replica: 1},
		"unknown type":         {Block: block, Key: AttrType, Lamport: 1, Replica: 1, Type: 12},
		"indent too deep":      {Block: block, Key: AttrIndent, Lamport: 1, Replica: 1, Indent: MaxIndent + 1},
		"language with space":  {Block: block, Key: AttrLanguage, Lamport: 1, Replica: 1, Language: "go lang"},
		"language with fence":  {Block: block, Key: AttrLanguage, Lamport: 1, Replica: 1, Language: "go`"},
		"other key's field":    {Block: block, Key: AttrChecked, Lamport: 1, Replica: 1, Checked: true, Indent: 1},
		"block counter zero":   {Block: crdt.ID{Replica: 3}, Key: AttrChecked, Lamport: 1, Replica: 1},
		"block replica zero":   {Block: crdt.ID{Counter: 3}, Key: AttrChecked, Lamport: 1, Replica: 1},
		"type write sets lang": {Block: block, Key: AttrType, Lamport: 1, Replica: 1, Type: Code, Language: "go"},
	}
	for name, w := range cases {
		if err := w.Validate(); !errors.Is(err, ErrInvalidAttr) {
			t.Errorf("%s: Validate = %v, want ErrInvalidAttr", name, err)
		}
	}
	if err := (AttrWrite{Block: crdt.Root, Key: AttrChecked, Lamport: 1, Replica: 1, Checked: true}).Validate(); err != nil {
		t.Errorf("the leading block (root) must accept writes: %v", err)
	}
}

func TestAtKeepsOnlyWritesUpToTheTimestamp(t *testing.T) {
	block := crdt.ID{Replica: 7, Counter: 1}
	a := NewAttributes()
	mustApplyAttr(t, a, typeWrite(block, 4, 1, Heading1))
	mustApplyAttr(t, a, typeWrite(block, 9, 1, Heading2))
	for bound, want := range map[uint64]Type{3: Paragraph, 4: Heading1, 8: Heading1, 9: Heading2} {
		at := a.At(bound)
		if err := at.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		if got := at.Get(block).Type; got != want {
			t.Errorf("At(%d): %s, want %s", bound, got, want)
		}
	}
}

func TestDiffWritesOnlyChangedRegisters(t *testing.T) {
	block := crdt.ID{Replica: 7, Counter: 1}
	a := NewAttributes()
	mustApplyAttr(t, a, typeWrite(block, 1, 1, Code))
	writes := a.Diff(block, Attrs{Type: Code, Language: "rust"}, 2, 1)
	if len(writes) != 1 || writes[0].Key != AttrLanguage || writes[0].Language != "rust" {
		t.Fatalf("Diff = %+v, want one language write", writes)
	}
}

// Strong eventual consistency for registers: stores that applied the same
// writes in any order, with duplicates, agree on every block.
func TestAttributesConvergeUnderAnyOrderAndDuplication(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	for round := 0; round < 200; round++ {
		var writes []AttrWrite
		blocksUsed := []crdt.ID{crdt.Root, {Replica: 5, Counter: 1}, {Replica: 6, Counter: 2}}
		for i := 0; i < 1+r.IntN(20); i++ {
			w := AttrWrite{Block: pick(r, blocksUsed), Lamport: uint64(1 + r.IntN(4)), Replica: crdt.ReplicaID(1 + r.IntN(3))}
			switch w.Key = AttrKey(1 + r.IntN(4)); w.Key {
			case AttrType:
				w.Type = Type(1 + r.IntN(11))
			case AttrIndent:
				w.Indent = uint32(r.IntN(4))
			case AttrChecked:
				w.Checked = r.IntN(2) == 0
			case AttrLanguage:
				w.Language = pick(r, []string{"", "go", "rust", "sql"})
			}
			writes = append(writes, w)
		}
		var stores []*Attributes
		for k := 0; k < 4; k++ {
			order := slices.Clone(writes)
			r.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
			if k%2 == 1 {
				order = append(order, order[:len(order)/2]...)
			}
			a := NewAttributes()
			for _, w := range order {
				mustApplyAttr(t, a, w)
			}
			stores = append(stores, a)
		}
		for _, b := range blocksUsed {
			for k := 1; k < len(stores); k++ {
				if stores[k].Get(b) != stores[0].Get(b) {
					t.Fatalf("round %d block %s: %+v != %+v", round, b, stores[k].Get(b), stores[0].Get(b))
				}
			}
		}
	}
}

func TestCodecGoldenBytes(t *testing.T) {
	writes := []AttrWrite{
		{Block: crdt.ID{Replica: 1, Counter: 1}, Key: AttrType, Lamport: 2, Replica: 1, Type: Heading2},
		{Block: crdt.ID{Replica: 300, Counter: 5}, Key: AttrLanguage, Lamport: 7, Replica: 300, Language: "go"},
		{Block: crdt.Root, Key: AttrChecked, Lamport: 3, Replica: 2, Checked: true},
		{Block: crdt.ID{Replica: 1, Counter: 9}, Key: AttrIndent, Lamport: 4, Replica: 1, Indent: 130 - 2},
	}
	want := "534101" + "04" +
		"01" + "01" + "01" + "02" + "01" + "03" +
		"04" + "ac02" + "05" + "07" + "ac02" + "02" + "676f" +
		"03" + "00" + "00" + "03" + "02" + "01" +
		"02" + "01" + "09" + "04" + "01" + "8001"
	got := EncodeAttrs(writes)
	if hex.EncodeToString(got) != want {
		t.Fatalf("encoding\n got %x\nwant %s", got, want)
	}
	back, err := DecodeAttrs(got)
	if err != nil || !slices.Equal(back, writes) {
		t.Fatalf("decode: %v %+v", err, back)
	}
}

func TestDecodeRejectsMalformedBytes(t *testing.T) {
	valid := EncodeAttrs([]AttrWrite{typeWrite(crdt.ID{Replica: 1, Counter: 1}, 1, 1, Code)})
	cases := map[string][]byte{
		"empty":           {},
		"wrong magic":     append([]byte("SK"), valid[2:]...),
		"wrong version":   append([]byte{'S', 'A', 2}, valid[3:]...),
		"truncated":       valid[:len(valid)-1],
		"trailing byte":   append(slices.Clone(valid), 0),
		"huge count":      {'S', 'A', 1, 0xff, 0xff, 0xff, 0xff, 0x0f},
		"non-minimal":     {'S', 'A', 1, 0x81, 0x00, 1, 1, 1, 1, 1, 9},
		"checked byte 2":  {'S', 'A', 1, 1, 3, 1, 1, 1, 1, 2},
		"unknown key":     {'S', 'A', 1, 1, 9, 1, 1, 1, 1, 0},
		"invalid type":    {'S', 'A', 1, 1, 1, 1, 1, 1, 1, 0},
		"language length": {'S', 'A', 1, 1, 4, 1, 1, 1, 1, 5, 'g'},
	}
	for name, data := range cases {
		if _, err := DecodeAttrs(data); !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrInvalidAttr) {
			t.Errorf("%s: err %v, want ErrCorrupt or ErrInvalidAttr", name, err)
		}
	}
}

func FuzzDecodeAttrs(f *testing.F) {
	f.Add(EncodeAttrs([]AttrWrite{typeWrite(crdt.ID{Replica: 1, Counter: 1}, 1, 1, Code)}))
	f.Add(EncodeAttrs([]AttrWrite{{Block: crdt.Root, Key: AttrLanguage, Lamport: 2, Replica: 3, Language: "python"}}))
	f.Add([]byte{'S', 'A', 1, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		writes, err := DecodeAttrs(data)
		if err != nil {
			return
		}
		// Anything accepted is valid, applies cleanly and re-encodes to the
		// same bytes: the encoding is unique.
		a := NewAttributes()
		for _, w := range writes {
			if _, err := a.Apply(w); err != nil {
				t.Fatalf("decoded write rejected: %v", err)
			}
		}
		if err := a.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		if again := EncodeAttrs(writes); !bytes.Equal(again, data) {
			t.Fatalf("re-encoding differs:\n%x\n%x", data, again)
		}
	})
}
