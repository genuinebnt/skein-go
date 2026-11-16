package trigram

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"
)

func oracleValues(set map[uint32]bool) []uint32 {
	out := make([]uint32, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}

func checkBitmap(t *testing.T, b *Bitmap, want map[uint32]bool) {
	t.Helper()
	if err := b.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	if got := b.ToArray(); !slices.Equal(got, oracleValues(want)) {
		t.Fatalf("bitmap holds %d values, oracle %d", len(got), len(want))
	}
	if b.Len() != len(want) {
		t.Fatalf("Len %d, want %d", b.Len(), len(want))
	}
}

// randomValue draws values clustered in a few chunks, so containers fill
// past the array limit and convert to bitsets and back.
func randomValue(r *rand.Rand) uint32 {
	chunk := uint32(r.IntN(3))
	if r.IntN(10) == 0 {
		chunk = uint32(r.IntN(1 << 16))
	}
	return chunk<<16 | uint32(r.IntN(6000))
}

func TestBitmapMatchesSetOracleUnderRandomOperations(t *testing.T) {
	r := newRand(testSeed(t))
	b := &Bitmap{}
	want := map[uint32]bool{}
	for step := range 30000 {
		v := randomValue(r)
		// Phases of mostly adds then mostly removes push one chunk across the
		// 4096 boundary in both directions.
		adding := (step/7000)%2 == 0
		if adding == (r.IntN(10) < 8) {
			if got := b.Add(v); got == want[v] {
				t.Fatalf("Add(%d) = %v with value present %v", v, got, want[v])
			}
			want[v] = true
		} else {
			if got := b.Remove(v); got != want[v] {
				t.Fatalf("Remove(%d) = %v with value present %v", v, got, want[v])
			}
			delete(want, v)
		}
		if b.Contains(v) != want[v] {
			t.Fatalf("Contains(%d) disagrees", v)
		}
		if step%500 == 0 {
			checkBitmap(t, b, want)
		}
		if err := b.CheckInvariants(); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
	}
	checkBitmap(t, b, want)
}

func randomBitmap(r *rand.Rand) (*Bitmap, map[uint32]bool) {
	b := &Bitmap{}
	want := map[uint32]bool{}
	n := []int{0, 10, 3000, 9000}[r.IntN(4)]
	for range n {
		v := randomValue(r)
		b.Add(v)
		want[v] = true
	}
	return b, want
}

func TestSetOperationsMatchTheOracle(t *testing.T) {
	r := newRand(testSeed(t))
	for range 60 {
		a, wa := randomBitmap(r)
		b, wb := randomBitmap(r)
		and, or, andNot := map[uint32]bool{}, map[uint32]bool{}, map[uint32]bool{}
		for v := range wa {
			or[v] = true
			if wb[v] {
				and[v] = true
			} else {
				andNot[v] = true
			}
		}
		for v := range wb {
			or[v] = true
		}
		checkBitmap(t, And(a, b), and)
		checkBitmap(t, Or(a, b), or)
		checkBitmap(t, AndNot(a, b), andNot)
		if !Or(a, b).Equal(Or(b, a)) {
			t.Fatal("Or is not symmetric")
		}
		checkBitmap(t, a, wa) // operands are not modified
	}
}

func TestNilBitmapIsEmpty(t *testing.T) {
	var b *Bitmap
	if !b.IsEmpty() || b.Len() != 0 || b.Contains(1) || !And(b, BitmapOf(1)).IsEmpty() || !Or(b, BitmapOf(1)).Equal(BitmapOf(1)) {
		t.Fatal("nil bitmap does not behave as empty")
	}
}

func TestBitmapEncodingMatchesThePortableRoaringFormat(t *testing.T) {
	// cookie 12346, 2 containers; (key 0, card-1 1), (key 1, card-1 0);
	// offsets 24 and 28; values 1, 2 and 70000 & 0xFFFF = 4464.
	want, _ := hex.DecodeString("3a30000002000000" + "00000100" + "01000000" + "18000000" + "1c000000" + "01000200" + "7011")
	got, err := BitmapOf(70000, 2, 1).MarshalBinary()
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("encoding %x, want %x", got, want)
	}
	empty, _ := (&Bitmap{}).MarshalBinary()
	if hex.EncodeToString(empty) != "3a30000000000000" {
		t.Fatalf("empty encoding %x", empty)
	}
}

func TestBitmapRoundTripsThroughItsEncoding(t *testing.T) {
	r := newRand(testSeed(t))
	for range 40 {
		b, want := randomBitmap(r)
		data, err := b.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		var back Bitmap
		if err := back.UnmarshalBinary(data); err != nil {
			t.Fatal(err)
		}
		checkBitmap(t, &back, want)
		again, _ := back.MarshalBinary()
		if !bytes.Equal(again, data) {
			t.Fatal("re-encoding differs")
		}
	}
}

func TestBitmapDecodingRejectsMalformedInput(t *testing.T) {
	good, _ := BitmapOf(1, 2, 70000).MarshalBinary()
	dense := &Bitmap{}
	for v := range uint32(5000) {
		dense.Add(v)
	}
	denseData, _ := dense.MarshalBinary()
	badPop := slices.Clone(denseData)
	badPop[len(badPop)-1] = 0xFF // sets bits the header does not count
	cases := map[string][]byte{
		"empty":           {},
		"bad cookie":      append([]byte{0, 0, 0, 0}, good[4:]...),
		"truncated":       good[:len(good)-1],
		"trailing":        append(slices.Clone(good), 0),
		"keys descending": func() []byte { d := slices.Clone(good); d[8], d[12] = 1, 0; return d }(),
		"array unordered": func() []byte { d := slices.Clone(good); d[24], d[26] = 2, 1; return d }(),
		"bad offset":      func() []byte { d := slices.Clone(good); d[16] = 99; return d }(),
		"wrong popcount":  badPop,
	}
	for name, data := range cases {
		var b Bitmap
		if err := b.UnmarshalBinary(data); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: error %v, want ErrCorrupt", name, err)
		}
	}
}

func FuzzBitmapUnmarshal(f *testing.F) {
	good, _ := BitmapOf(1, 2, 70000).MarshalBinary()
	f.Add(good)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var b Bitmap
		if err := b.UnmarshalBinary(data); err != nil {
			return
		}
		if err := b.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		again, _ := b.MarshalBinary()
		if !bytes.Equal(again, data) {
			t.Fatal("accepted a non-canonical encoding")
		}
	})
}
