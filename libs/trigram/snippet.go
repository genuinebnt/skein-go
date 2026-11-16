package trigram

import (
	"fmt"
	"slices"

	"github.com/genuinebnt/skein-go/libs/dp"
)

// Snippet is a window of a document's text to show for a hit.
type Snippet struct {
	// Text is the window's text.
	Text string
	// Window is where Text lies in the document, in code points.
	Window Span
	// Highlights are the matches that overlap the window, clipped to it,
	// in code points relative to Text, ascending.
	Highlights []Span
	// Focus is the match the snippet was built around, in document code
	// points: the first match inside the span dp.BestSnippet chose. Zero
	// when there are no matches.
	Focus Span
}

// MakeSnippet chooses a window of at most window code points of text to
// show for matches. Each match carries a term number (dp.Match.Term); the
// window is centred on the span dp.BestSnippet picks (the most distinct
// terms, then the shortest span, then the earliest), widened equally on
// both sides and shifted to stay inside the text. With no match, or when
// no match fits in the window, it starts at the first match (or the text's
// start).
//
// A negative window returns ErrInvalidArgument; matches must lie inside
// the text.
func MakeSnippet(text string, matches []dp.Match, window int) (Snippet, error) {
	if window < 0 {
		return Snippet{}, fmt.Errorf("%w: window %d", ErrInvalidArgument, window)
	}
	runes := []rune(text)
	n := len(runes)
	for _, m := range matches {
		if m.Start < 0 || m.End < m.Start || m.End > n {
			return Snippet{}, fmt.Errorf("%w: match %d..%d outside text of %d code points", ErrInvalidArgument, m.Start, m.End, n)
		}
	}
	best, ok, err := dp.BestSnippet(matches, window)
	if err != nil {
		return Snippet{}, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	var core Span
	switch {
	case ok:
		core = Span{best.Span.Start, best.Span.End}
	case len(matches) > 0:
		first := slices.MinFunc(matches, func(a, b dp.Match) int {
			if a.Start != b.Start {
				return a.Start - b.Start
			}
			return a.End - b.End
		})
		core = Span{first.Start, first.Start}
	}
	start := core.Start - (window-(core.End-core.Start))/2
	start = max(0, min(start, n-window))
	end := min(n, start+window)
	s := Snippet{Text: string(runes[start:end]), Window: Span{start, end}}
	focusSet := false
	for _, m := range sortedMatches(matches) {
		if !focusSet && m.Start >= core.Start && (!ok || m.End <= core.End) {
			s.Focus, focusSet = Span{m.Start, m.End}, true
		}
		switch {
		case m.Start == m.End && m.Start >= start && m.Start <= end:
			s.Highlights = append(s.Highlights, Span{m.Start - start, m.Start - start})
		case m.Start < end && m.End > start:
			s.Highlights = append(s.Highlights, Span{max(m.Start, start) - start, min(m.End, end) - start})
		}
	}
	return s, nil
}

func sortedMatches(matches []dp.Match) []dp.Match {
	out := slices.Clone(matches)
	slices.SortFunc(out, func(a, b dp.Match) int {
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		if a.End != b.End {
			return a.End - b.End
		}
		return a.Term - b.Term
	})
	return out
}
