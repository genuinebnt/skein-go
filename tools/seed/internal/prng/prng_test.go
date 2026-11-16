package prng

import "testing"

func TestStreamsDependOnlyOnSeedLabelAndIndex(t *testing.T) {
	a := New(1).Stream("x", 3)
	_ = New(1).Stream("y", 3).Uint64() // drawing elsewhere must not matter
	b := New(1).Stream("x", 3)
	for i := 0; i < 100; i++ {
		if a.Uint64() != b.Uint64() {
			t.Fatal("same stream gave different values")
		}
	}
	if New(1).Stream("x", 3).Uint64() == New(1).Stream("x", 4).Uint64() || New(1).Stream("x", 3).Uint64() == New(2).Stream("x", 3).Uint64() {
		t.Fatal("different streams gave the same first value")
	}
}

func TestZipfFavoursLowRanks(t *testing.T) {
	z := NewZipf(50, 1.2)
	r := New(9).Stream("z", 0)
	counts := make([]int, 50)
	for i := 0; i < 20000; i++ {
		counts[z.Draw(r)]++
	}
	if counts[0] < counts[1] || counts[1] < counts[10] || counts[10] < counts[49] {
		t.Fatalf("counts not decreasing: %v", counts[:12])
	}
}

func TestSampleReturnsDistinctIndexes(t *testing.T) {
	r := New(5).Stream("s", 0)
	seen := map[int]bool{}
	for _, i := range r.Sample(100, 40) {
		if seen[i] || i < 0 || i >= 100 {
			t.Fatalf("bad or repeated index %d", i)
		}
		seen[i] = true
	}
	if len(r.Sample(3, 10)) != 3 {
		t.Fatal("sample larger than population not clamped")
	}
}
