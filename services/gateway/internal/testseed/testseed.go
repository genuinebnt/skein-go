// Package testseed gives randomised tests a reproducible seed: every test
// logs the seed it used, and SKEIN_SEED replays a failure exactly
// (docs/testing.md). It copies platform/internal/testseed, which another
// module cannot import.
package testseed

import (
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
)

// Seed returns SKEIN_SEED when set, otherwise a fresh random seed, and logs
// it so a failing run can be replayed.
func Seed(t testing.TB) uint64 {
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

// Rand returns a PCG generator for seed, the same construction libs/crdt
// uses, so seeds behave alike across the repository.
func Rand(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
}
