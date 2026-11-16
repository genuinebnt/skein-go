package trigram

import (
	"fmt"
	"testing"
)

// benchIndex builds an index of n synthetic code-and-prose blocks, the
// shape of a medium seed workspace.
func benchIndex(b *testing.B, n int) *Index {
	b.Helper()
	r := newRand(42)
	words := []string{"raft", "log", "compaction", "snapshot", "leader", "follower", "term", "commit", "index", "store", "context", "error"}
	ix := NewIndex()
	for i := range n {
		text := ""
		for range 30 {
			text += words[r.IntN(len(words))] + " "
		}
		if i%5 == 0 {
			text = fmt.Sprintf("func (s *Store%d) Close(ctx context.Context) error { return nil }", i)
		}
		_ = ix.Add(DocID(i), Doc{Text: text, Code: i%5 == 0, Language: "go"})
	}
	return ix
}

func BenchmarkAdd10k(b *testing.B) {
	for b.Loop() {
		benchIndex(b, 10_000)
	}
}

func BenchmarkLiteral10k(b *testing.B) {
	ix := benchIndex(b, 10_000)
	for b.Loop() {
		ix.SearchLiteral("compaction snapshot", Options{})
	}
}

func BenchmarkRegexp10k(b *testing.B) {
	ix := benchIndex(b, 10_000)
	re, _ := CompileRegexp(`func \(\w+ \*Store\d+\) Close\(ctx context\.Context\)`, true)
	for b.Loop() {
		ix.SearchCompiled(re, Options{Languages: []string{"go"}})
	}
}

func BenchmarkSnapshot10k(b *testing.B) {
	ix := benchIndex(b, 10_000)
	for b.Loop() {
		data, _ := ix.MarshalBinary()
		_, _ = UnmarshalIndex(data)
	}
}
