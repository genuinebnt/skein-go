package crdt

import "testing"

// Baselines for the reference implementation. The optimised representation
// (runs plus an order-statistic tree) must beat these while passing the same
// tests. Record results in docs/PERF.md with the machine and Go version.

func BenchmarkAppendTyping(b *testing.B) {
	for range b.N {
		s := newSeq(b, 1)
		for i := 0; i < 2000; i++ {
			if _, err := s.Insert(i, "x"); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkRandomEdits(b *testing.B) {
	r := newRand(1)
	for range b.N {
		s := newSeq(b, 1)
		for i := 0; i < 2000; i++ {
			randomEdit(b, r, s)
		}
	}
}

func BenchmarkMergeConcurrentHistories(b *testing.B) {
	r := newRand(2)
	a, c := newSeq(b, 1), newSeq(b, 2)
	for i := 0; i < 1000; i++ {
		randomEdit(b, r, a)
		randomEdit(b, r, c)
	}
	opsA, opsC := a.OpsSince(nil), c.OpsSince(nil)
	b.ResetTimer()
	for range b.N {
		s := newSeq(b, 3)
		for _, op := range opsA {
			if err := s.Apply(op); err != nil {
				b.Fatal(err)
			}
		}
		for _, op := range opsC {
			if err := s.Apply(op); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkEncodeDecode(b *testing.B) {
	r := newRand(3)
	s := newSeq(b, 1)
	for i := 0; i < 2000; i++ {
		randomEdit(b, r, s)
	}
	ops := s.OpsSince(nil)
	b.ResetTimer()
	for range b.N {
		if _, err := DecodeOps(EncodeOps(ops)); err != nil {
			b.Fatal(err)
		}
	}
}
