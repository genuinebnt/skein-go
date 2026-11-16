package dp

import "fmt"

// MaxChunkTokens bounds the chunk limit so that the cost of a chunking
// (blocks × limit², squared slack per chunk) fits in int64 for any
// realistic number of blocks.
const MaxChunkTokens = 1 << 20

// Chunking is the result of ChunkBlocks.
type Chunking struct {
	// Ends holds, for each chunk in order, the index one past its last
	// block: chunk k covers blocks [Ends[k-1], Ends[k]), with Ends[-1] = 0.
	Ends []int
	// Cost is the sum over chunks of (limit - tokens in chunk)².
	Cost int64
}

// ChunkBlocks splits a sequence of blocks with the given token counts into
// contiguous chunks of at most limit tokens each, minimising the sum of
// squared slack, (limit - chunk tokens)², over all chunks. The ml service
// uses it to cut notes into embedding chunks: squaring makes the DP prefer
// several evenly filled chunks to one full chunk and one nearly empty one,
// and blocks are never split, so a paragraph or code block stays whole.
//
// This is the text-justification DP (Knuth and Plass, simplified), with one
// difference: the last chunk's slack counts too, because a tiny trailing
// chunk embeds as poorly as a tiny chunk anywhere else.
//
// Recurrence over suffixes, with tokens(i, j) the tokens of blocks i..j-1:
//
//	cost(n) = 0
//	cost(i) = min over j > i with tokens(i, j) <= limit of
//	          (limit - tokens(i, j))² + cost(j)
//
// Ties (equal cost) go to fewer chunks, then to the longer first chunk;
// both are decided locally because the rest is the same subproblem.
//
// Time O(n·w), where w is the largest number of consecutive blocks that fit
// in one chunk. Memory O(n). Token counts must be non-negative and limit in
// 1..MaxChunkTokens (ErrInvalidInput); a block over the limit makes chunking
// impossible (ErrBlockTooLarge). No blocks give no chunks.
func ChunkBlocks(tokens []int, limit int) (Chunking, error) {
	if limit < 1 || limit > MaxChunkTokens {
		return Chunking{}, fmt.Errorf("%w: chunk limit %d is outside 1..%d", ErrInvalidInput, limit, MaxChunkTokens)
	}
	for i, t := range tokens {
		if t < 0 {
			return Chunking{}, fmt.Errorf("%w: block %d has %d tokens", ErrInvalidInput, i, t)
		}
		if t > limit {
			return Chunking{}, fmt.Errorf("%w: block %d has %d tokens, limit %d", ErrBlockTooLarge, i, t, limit)
		}
	}
	n := len(tokens)
	cost := make([]int64, n+1)
	chunks := make([]int, n+1)
	end := make([]int, n+1)
	for i := n - 1; i >= 0; i-- {
		cost[i], chunks[i], end[i] = -1, 0, 0
		sum := 0
		for j := i + 1; j <= n; j++ {
			sum += tokens[j-1]
			if sum > limit {
				break
			}
			slack := int64(limit - sum)
			c := slack*slack + cost[j]
			if cost[i] < 0 || c < cost[i] || (c == cost[i] && chunks[j]+1 <= chunks[i]) {
				cost[i], chunks[i], end[i] = c, chunks[j]+1, j
			}
		}
	}
	var ends []int
	for i := 0; i < n; i = end[i] {
		ends = append(ends, end[i])
	}
	return Chunking{Ends: ends, Cost: cost[0]}, nil
}

// CheckChunks verifies that c is a valid chunking of tokens under limit:
// chunk ends strictly increase, the last is len(tokens), no chunk exceeds
// the limit and Cost is the sum of squared slack. It does not check
// optimality. Errors wrap ErrInvariant.
func CheckChunks(tokens []int, limit int, c Chunking) error {
	start := 0
	var cost int64
	for k, end := range c.Ends {
		if end <= start || end > len(tokens) {
			return fmt.Errorf("%w: chunk %d ends at %d after starting at %d (of %d blocks)", ErrInvariant, k, end, start, len(tokens))
		}
		sum := 0
		for _, t := range tokens[start:end] {
			sum += t
		}
		if sum > limit {
			return fmt.Errorf("%w: chunk %d has %d tokens, limit %d", ErrInvariant, k, sum, limit)
		}
		slack := int64(limit - sum)
		cost += slack * slack
		start = end
	}
	if start != len(tokens) {
		return fmt.Errorf("%w: chunks cover %d of %d blocks", ErrInvariant, start, len(tokens))
	}
	if cost != c.Cost {
		return fmt.Errorf("%w: cost is %d, result says %d", ErrInvariant, cost, c.Cost)
	}
	return nil
}
