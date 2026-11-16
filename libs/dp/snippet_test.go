package dp

import (
	"errors"
	"slices"
	"testing"
)

// oracleSnippet tries every span [s, e) with e - s <= window over the range
// of positions and returns the best by the rules on BestSnippet.
func oracleSnippet(matches []Match, window int) (Snippet, bool) {
	limit := 0
	for _, m := range matches {
		limit = max(limit, m.End)
	}
	var best Snippet
	for s := 0; s <= limit; s++ {
		for e := s; e <= s+window && e <= limit; e++ {
			terms := map[int]bool{}
			var inside []Match
			for _, m := range matches {
				if m.Start >= s && m.End <= e {
					terms[m.Term] = true
					inside = append(inside, m)
				}
			}
			length, bestLength := e-s, best.Span.End-best.Span.Start
			if len(terms) > best.Terms || (len(terms) == best.Terms && len(terms) > 0 && length < bestLength) {
				slices.SortFunc(inside, compareMatchByStart)
				best = Snippet{Span: Span{s, e}, Terms: len(terms), Matches: inside}
			}
		}
	}
	return best, best.Terms > 0
}

func TestBestSnippetMatchesAllWindowsOracle(t *testing.T) {
	r := newRand(testSeed(t))
	for trial := 0; trial < 1500; trial++ {
		matches := make([]Match, r.IntN(9))
		for i := range matches {
			start := r.IntN(30)
			matches[i] = Match{Term: r.IntN(4), Start: start, End: start + r.IntN(5)}
		}
		window := r.IntN(15)
		got, ok, err := BestSnippet(matches, window)
		if err != nil {
			t.Fatal(err)
		}
		want, wantOK := oracleSnippet(matches, window)
		if ok != wantOK || got.Span != want.Span || got.Terms != want.Terms || !slices.Equal(got.Matches, want.Matches) {
			t.Fatalf("BestSnippet(%v, %d) = %+v, %t; oracle %+v, %t", matches, window, got, ok, want, wantOK)
		}
	}
}

func TestBestSnippetPrefersDistinctTermsOverRepeats(t *testing.T) {
	matches := []Match{
		{Term: 0, Start: 0, End: 4}, {Term: 0, Start: 5, End: 9}, {Term: 0, Start: 10, End: 14},
		{Term: 0, Start: 100, End: 104}, {Term: 1, Start: 120, End: 125},
	}
	got, ok, err := BestSnippet(matches, 40)
	if err != nil || !ok {
		t.Fatalf("got %v, %v", ok, err)
	}
	if got.Span != (Span{100, 125}) || got.Terms != 2 || len(got.Matches) != 2 {
		t.Fatalf("got %+v, want the span with both terms", got)
	}
}

func TestBestSnippetWithNothingThatFits(t *testing.T) {
	if _, ok, err := BestSnippet(nil, 10); ok || err != nil {
		t.Fatalf("no matches: ok=%t err=%v", ok, err)
	}
	if _, ok, err := BestSnippet([]Match{{Term: 0, Start: 0, End: 20}}, 10); ok || err != nil {
		t.Fatalf("match longer than window: ok=%t err=%v", ok, err)
	}
	if _, _, err := BestSnippet([]Match{{Term: 0, Start: 5, End: 4}}, 10); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reversed match: err=%v", err)
	}
	if _, _, err := BestSnippet(nil, -1); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("negative window: err=%v", err)
	}
}
