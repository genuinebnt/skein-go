package dp

import (
	"errors"
	"slices"
	"testing"
)

// oracleChunks tries every way to split tokens into contiguous chunks and
// returns the best by the rules on ChunkBlocks: lowest cost, then fewest
// chunks, then the lexicographically largest list of chunk ends.
func oracleChunks(tokens []int, limit int) (Chunking, bool) {
	n := len(tokens)
	if n == 0 {
		return Chunking{}, true
	}
	var best Chunking
	found := false
	// Bit i of mask set means a chunk ends after block i (for i < n-1).
	for mask := 0; mask < 1<<(n-1); mask++ {
		var ends []int
		for i := 0; i < n-1; i++ {
			if mask&(1<<i) != 0 {
				ends = append(ends, i+1)
			}
		}
		ends = append(ends, n)
		c := Chunking{Ends: ends}
		start, ok := 0, true
		for _, end := range ends {
			sum := 0
			for _, t := range tokens[start:end] {
				sum += t
			}
			if sum > limit {
				ok = false
				break
			}
			c.Cost += int64(limit-sum) * int64(limit-sum)
			start = end
		}
		if !ok {
			continue
		}
		better := !found || c.Cost < best.Cost ||
			(c.Cost == best.Cost && len(c.Ends) < len(best.Ends)) ||
			(c.Cost == best.Cost && len(c.Ends) == len(best.Ends) && slices.Compare(c.Ends, best.Ends) > 0)
		if better {
			best, found = c, true
		}
	}
	return best, found
}

func TestChunkBlocksMatchesAllSplitsOracle(t *testing.T) {
	r := newRand(testSeed(t))
	for trial := 0; trial < 1000; trial++ {
		limit := 1 + r.IntN(12)
		tokens := make([]int, r.IntN(13))
		for i := range tokens {
			tokens[i] = r.IntN(limit + 1)
		}
		got, err := ChunkBlocks(tokens, limit)
		if err != nil {
			t.Fatalf("ChunkBlocks(%v, %d): %v", tokens, limit, err)
		}
		if err := CheckChunks(tokens, limit, got); err != nil {
			t.Fatalf("ChunkBlocks(%v, %d) = %+v: %v", tokens, limit, got, err)
		}
		want, _ := oracleChunks(tokens, limit)
		if got.Cost != want.Cost || !slices.Equal(got.Ends, want.Ends) {
			t.Fatalf("ChunkBlocks(%v, %d) = %+v, oracle %+v", tokens, limit, got, want)
		}
	}
}

func TestChunkBlocksPrefersEvenChunksToGreedyFilling(t *testing.T) {
	// Greedy fills 10 then leaves 2 (slack 0 + 64 = 64); even chunks of 6
	// and 6 cost 16 + 16 = 32.
	got, err := ChunkBlocks([]int{4, 2, 4, 2}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Ends, []int{2, 4}) || got.Cost != 32 {
		t.Fatalf("got %+v, want ends [2 4] cost 32", got)
	}
}

func TestChunkBlocksRejectsImpossibleInput(t *testing.T) {
	if _, err := ChunkBlocks([]int{3, 11, 2}, 10); !errors.Is(err, ErrBlockTooLarge) {
		t.Errorf("oversized block: err = %v", err)
	}
	if _, err := ChunkBlocks([]int{1}, 0); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("zero limit: err = %v", err)
	}
	if _, err := ChunkBlocks([]int{-1}, 5); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("negative tokens: err = %v", err)
	}
	if got, err := ChunkBlocks(nil, 5); err != nil || len(got.Ends) != 0 || got.Cost != 0 {
		t.Errorf("no blocks: got %+v, %v", got, err)
	}
}

func TestCheckChunksRejectsInvalidChunkings(t *testing.T) {
	tokens := []int{3, 3, 3}
	for name, c := range map[string]Chunking{
		"over limit":   {Ends: []int{3}, Cost: 0},
		"gap":          {Ends: []int{1}, Cost: 9},
		"not ordered":  {Ends: []int{2, 1, 3}, Cost: 0},
		"wrong cost":   {Ends: []int{1, 2, 3}, Cost: 1},
		"past the end": {Ends: []int{1, 4}, Cost: 0},
	} {
		if err := CheckChunks(tokens, 6, c); !errors.Is(err, ErrInvariant) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
