package dp

import (
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
)

// testSeed returns the seed for randomised tests and logs it. Set
// SKEIN_SEED to replay a failure.
func testSeed(t testing.TB) uint64 {
	t.Helper()
	seed := rand.Uint64()
	if v := os.Getenv("SKEIN_SEED"); v != "" {
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("SKEIN_SEED=%q: %v", v, err)
		}
		seed = parsed
	}
	t.Logf("seed %d (replay with SKEIN_SEED=%d)", seed, seed)
	return seed
}

func newRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)) }

// randomString returns up to maxLen runes from alphabet. A small alphabet
// makes matches, repeats and ties common; multi-byte runes in it catch
// byte/rune confusion.
func randomString(r *rand.Rand, alphabet []rune, maxLen int) string {
	out := make([]rune, r.IntN(maxLen+1))
	for i := range out {
		out[i] = alphabet[r.IntN(len(alphabet))]
	}
	return string(out)
}

func randomInts(r *rand.Rand, alphabet, maxLen int) []int {
	out := make([]int, r.IntN(maxLen+1))
	for i := range out {
		out[i] = r.IntN(alphabet)
	}
	return out
}

var smallAlphabet = []rune("abé🙂")
