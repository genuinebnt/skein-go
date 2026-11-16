package blocks

import (
	"math/rand/v2"
	"os"
	"strconv"
	"testing"

	"github.com/genuinebnt/skein-go/libs/crdt"
)

// testSeed returns the seed for randomised tests. Set SKEIN_SEED to replay a
// failure; every randomised test logs the seed it used.
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

func newDoc(t testing.TB, r crdt.ReplicaID) *Document {
	t.Helper()
	d, err := NewDocument(r)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// mustValid checks every invariant of the document.
func mustValid(t testing.TB, d *Document) {
	t.Helper()
	if err := d.CheckInvariants(); err != nil {
		t.Fatalf("replica %d: %v", d.Sequence().Replica(), err)
	}
}

func mustBlocks(t testing.TB, d *Document) []Block {
	t.Helper()
	bs, err := d.Blocks()
	if err != nil {
		t.Fatal(err)
	}
	return bs
}

// pick returns a random element of xs.
func pick[T any](r *rand.Rand, xs []T) T { return xs[r.IntN(len(xs))] }

// randomRunes returns up to max code points from alphabet.
func randomRunes(r *rand.Rand, alphabet []rune, max int) string {
	n := r.IntN(max + 1)
	out := make([]rune, n)
	for i := range out {
		out[i] = pick(r, alphabet)
	}
	return string(out)
}

func contents(bs []Block) []Block {
	out := make([]Block, len(bs))
	for i, b := range bs {
		out[i] = b.Content()
	}
	return out
}
