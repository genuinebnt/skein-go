package dp

import (
	"fmt"
	"strings"
	"testing"
)

// Baselines for the reference implementations, recorded in docs/PERF.md.
// Workloads are fixed (seeded) so the Rust port can run the same ones.

func benchText(seed uint64, words int) string {
	r := newRand(seed)
	vocabulary := strings.Fields("the a rust async note graph search crdt block 漢字 café 🙂 diff plan chunk")
	var b strings.Builder
	for i := 0; i < words; i++ {
		b.WriteString(vocabulary[r.IntN(len(vocabulary))])
		if r.IntN(8) == 0 {
			b.WriteByte('\n')
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// benchRunes returns exactly n runes of sample text.
func benchRunes(seed uint64, n int) string {
	return string([]rune(benchText(seed, n))[:n])
}

// editedCopy changes roughly one word in fifty.
func editedCopy(seed uint64, text string) string {
	r := newRand(seed)
	words := strings.SplitAfter(text, " ")
	for i := range words {
		if r.IntN(50) == 0 {
			words[i] = "edited "
		}
	}
	return strings.Join(words, "")
}

func BenchmarkLevenshtein100(b *testing.B) {
	x, y := benchRunes(1, 100), benchRunes(2, 100)
	for range b.N {
		Levenshtein(x, y)
	}
}

func BenchmarkLevenshteinWithinK2Of100(b *testing.B) {
	x := benchRunes(1, 100)
	y := editedCopy(3, x)
	for range b.N {
		LevenshteinWithin(x, y, 2)
	}
}

func BenchmarkDamerauLevenshtein30(b *testing.B) {
	x, y := benchRunes(1, 30), benchRunes(2, 30)
	for range b.N {
		DamerauLevenshtein(x, y)
	}
}

func BenchmarkLCS2000(b *testing.B) {
	r := newRand(4)
	x, y := make([]int, 2000), make([]int, 2000)
	for i := range x {
		x[i], y[i] = r.IntN(20), r.IntN(20)
	}
	b.ResetTimer()
	for range b.N {
		LCS(x, y)
	}
}

func BenchmarkDiffLines(b *testing.B) {
	for _, words := range []int{2_000, 20_000} {
		a := benchText(5, words)
		c := editedCopy(6, a)
		b.Run(fmt.Sprintf("myers/%dwords", words), func(b *testing.B) {
			for range b.N {
				DiffLines(a, c)
			}
		})
		b.Run(fmt.Sprintf("patience/%dwords", words), func(b *testing.B) {
			for range b.N {
				PatienceDiffLines(a, c)
			}
		})
		b.Run(fmt.Sprintf("chars/%dwords", words), func(b *testing.B) {
			for range b.N {
				DiffChars(a, c)
			}
		})
	}
}

func BenchmarkPlanReading(b *testing.B) {
	r := newRand(7)
	forest := randomNotes(r, 300, true)
	dag := randomNotes(r, 40, false)
	b.Run("tree-dp/300notes/120min", func(b *testing.B) {
		for range b.N {
			if _, err := PlanReading(forest, 120, PlanOptions{}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("branch-and-bound/40notes/60min", func(b *testing.B) {
		for range b.N {
			if _, err := PlanReading(dag, 60, PlanOptions{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkChunkBlocks1000(b *testing.B) {
	r := newRand(8)
	tokens := make([]int, 1000)
	for i := range tokens {
		tokens[i] = 10 + r.IntN(120)
	}
	for range b.N {
		if _, err := ChunkBlocks(tokens, 512); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBestSnippet200(b *testing.B) {
	r := newRand(9)
	matches := make([]Match, 200)
	for i := range matches {
		start := r.IntN(5000)
		matches[i] = Match{Term: r.IntN(5), Start: start, End: start + 3 + r.IntN(6)}
	}
	for range b.N {
		if _, _, err := BestSnippet(matches, 160); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSegment(b *testing.B) {
	s := newTestSegmenter(b)
	for range b.N {
		s.Segment("rustasyncwebsocketgolang")
	}
}
