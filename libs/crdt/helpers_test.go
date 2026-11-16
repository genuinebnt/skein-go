package crdt

import (
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
)

// newSeq creates a sequence and fails the test on error.
func newSeq(t testing.TB, r ReplicaID) *Sequence {
	t.Helper()
	s, err := New(r)
	if err != nil {
		t.Fatalf("New(%d): %v", r, err)
	}
	return s
}

// mustInsert inserts text and checks every invariant afterwards.
func mustInsert(t testing.TB, s *Sequence, index int, text string) []Op {
	t.Helper()
	ops, err := s.Insert(index, text)
	if err != nil {
		t.Fatalf("Insert(%d, %q): %v", index, text, err)
	}
	mustValid(t, s)
	return ops
}

// mustDelete deletes and checks every invariant afterwards.
func mustDelete(t testing.TB, s *Sequence, index, count int) []Op {
	t.Helper()
	ops, err := s.Delete(index, count)
	if err != nil {
		t.Fatalf("Delete(%d, %d): %v", index, count, err)
	}
	mustValid(t, s)
	return ops
}

// mustApply applies ops in order, all of which must be ready.
func mustApply(t testing.TB, s *Sequence, ops []Op) {
	t.Helper()
	for _, op := range ops {
		if err := s.Apply(op); err != nil {
			t.Fatalf("Apply(%s): %v", op, err)
		}
	}
	mustValid(t, s)
}

func mustValid(t testing.TB, s *Sequence) {
	t.Helper()
	if err := s.CheckInvariants(); err != nil {
		t.Fatalf("replica %d: %v", s.Replica(), err)
	}
}

// syncAll brings every replica up to date with every other one.
func syncAll(t testing.TB, seqs ...*Sequence) {
	t.Helper()
	for _, to := range seqs {
		for _, from := range seqs {
			if from != to {
				mustApply(t, to, from.OpsSince(to.Version()))
			}
		}
	}
}

// testSeed returns the seed for randomized tests. Set SKEIN_SEED to replay a
// failure; every randomized test logs the seed it used.
func testSeed(t testing.TB) uint64 {
	t.Helper()
	if v := os.Getenv("SKEIN_SEED"); v != "" {
		seed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("SKEIN_SEED=%q: %v", v, err)
		}
		return seed
	}
	return rand.Uint64()
}

func newRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)) }

// randomText returns 1 to max runes drawn from a small alphabet that includes
// multi-byte characters, so byte/rune confusion shows up in tests.
func randomText(r *rand.Rand, max int) string {
	alphabet := []rune("abcdefxyzé漢🙂")
	n := 1 + r.IntN(max)
	out := make([]rune, n)
	for i := range out {
		out[i] = alphabet[r.IntN(len(alphabet))]
	}
	return string(out)
}

// randomEdit applies one random local edit to s and returns its ops.
func randomEdit(t testing.TB, r *rand.Rand, s *Sequence) []Op {
	t.Helper()
	if s.Len() > 0 && r.IntN(3) == 0 {
		index := r.IntN(s.Len())
		count := 1 + r.IntN(min(4, s.Len()-index))
		ops, err := s.Delete(index, count)
		if err != nil {
			t.Fatalf("Delete(%d, %d): %v", index, count, err)
		}
		return ops
	}
	ops, err := s.Insert(r.IntN(s.Len()+1), randomText(r, 4))
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return ops
}
