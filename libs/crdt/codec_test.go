package crdt

import (
	"bytes"
	"errors"
	"slices"
	"testing"
)

func TestCodecRoundTripsRandomHistories(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	net := newNetwork(t, r.Uint64(), 3)
	for step := 0; step < 200; step++ {
		i := r.IntN(3)
		net.broadcast(i, randomEdit(t, r, net.replicas[i]))
	}
	net.deliverAll(t)
	ops := net.replicas[0].OpsSince(nil)
	data := EncodeOps(ops)
	got, err := DecodeOps(data)
	if err != nil {
		t.Fatalf("DecodeOps: %v", err)
	}
	if !slices.Equal(got, ops) {
		t.Fatalf("decoded ops differ from the originals")
	}
	if !bytes.Equal(EncodeOps(got), data) {
		t.Fatalf("encoding is not canonical")
	}
}

func TestCodecEncodesEmptyBatch(t *testing.T) {
	data := EncodeOps(nil)
	if !bytes.Equal(data, []byte{'S', 'K', 1, 0}) {
		t.Fatalf("empty batch = %v", data)
	}
	ops, err := DecodeOps(data)
	if err != nil || len(ops) != 0 {
		t.Fatalf("DecodeOps(empty) = %v, %v", ops, err)
	}
}

// Pins the exact bytes of one insert and one delete. If this test changes,
// the wire format changed: bump codecVersion and update the spec and the
// Rust port.
func TestCodecGoldenBytes(t *testing.T) {
	ops := []Op{
		{ID: ID{1, 1}, Lamport: 1, Kind: OpInsert, Parent: Root, Side: SideRight, Value: 'h'},
		{ID: ID{300, 2}, Lamport: 5, Kind: OpDelete, Target: ID{1, 1}},
	}
	want := []byte{
		'S', 'K', 1, 2,
		1, 1, 1, 1, 0, 0, 1, 'h',
		2, 0xAC, 0x02, 2, 5, 1, 1,
	}
	if got := EncodeOps(ops); !bytes.Equal(got, want) {
		t.Fatalf("EncodeOps =\n%v\nwant\n%v", got, want)
	}
}

func TestDecodeRejectsMalformedInput(t *testing.T) {
	valid := EncodeOps([]Op{{ID: ID{1, 1}, Lamport: 1, Kind: OpInsert, Side: SideRight, Value: 'a'}})
	cases := map[string][]byte{
		"empty":             {},
		"bad magic":         {'X', 'K', 1, 0},
		"future version":    {'S', 'K', 2, 0},
		"count too large":   {'S', 'K', 1, 0xFF, 0xFF, 0xFF, 0xFF, 0x0F},
		"truncated op":      valid[:len(valid)-1],
		"trailing bytes":    append(slices.Clone(valid), 0),
		"non-minimal count": {'S', 'K', 1, 0x80, 0x00},
		"unknown kind":      {'S', 'K', 1, 1, 9, 1, 1, 1, 0, 0},
	}
	for name, data := range cases {
		if _, err := DecodeOps(data); err == nil {
			t.Errorf("%s: decoded without error", name)
		} else if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrInvalidOp) {
			t.Errorf("%s: error %v is neither ErrCorrupt nor ErrInvalidOp", name, err)
		}
	}
}

// FuzzDecodeOps checks that no input makes the decoder panic, and that
// anything it accepts re-encodes to exactly the same bytes.
func FuzzDecodeOps(f *testing.F) {
	f.Add(EncodeOps(nil))
	f.Add(EncodeOps([]Op{
		{ID: ID{1, 1}, Lamport: 1, Kind: OpInsert, Side: SideRight, Value: '🙂'},
		{ID: ID{1, 2}, Lamport: 2, Kind: OpDelete, Target: ID{1, 1}},
	}))
	f.Add([]byte{'S', 'K', 1, 0xFF, 0xFF})
	f.Fuzz(func(t *testing.T, data []byte) {
		ops, err := DecodeOps(data)
		if err != nil {
			return
		}
		if again := EncodeOps(ops); !bytes.Equal(again, data) {
			t.Fatalf("accepted input does not re-encode identically")
		}
	})
}

// FuzzApply feeds decoded ops from arbitrary bytes into a sequence. Whatever
// it accepts, the sequence must stay internally consistent.
func FuzzApply(f *testing.F) {
	s := newSeq(f, 1)
	ops, _ := s.Insert(0, "seed")
	f.Add(EncodeOps(ops))
	f.Fuzz(func(t *testing.T, data []byte) {
		ops, err := DecodeOps(data)
		if err != nil {
			return
		}
		s := newSeq(t, 50)
		_ = NewCausalBuffer(s, 64).Deliver(ops...)
		if err := s.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
	})
}
