package domain

import (
	"slices"
	"strings"

	"github.com/genuinebnt/skein-go/libs/dp"
	"github.com/genuinebnt/skein-go/libs/trigram"
)

// RegexMatch is one block matched by a regex search, before rendering.
type RegexMatch struct {
	Ref     BlockRef
	Ordinal int
	Spans   []trigram.Span
}

// RegexResults are the blocks a regex search matched, in note ID order and
// then block order, the order the seed truth lists matches in.
type RegexResults struct {
	Matches    []RegexMatch
	Candidates int
}

// SearchRegex runs a compiled pattern over the readable notes' blocks: the
// readable blocks (a bitmap, BlocksOf) are intersected with the pattern's
// trigram candidates and the code filters before any block is read.
func (w *Workspace) SearchRegex(re *trigram.Regexp, readable Readable, f Filters) RegexResults {
	within := w.BlocksOf(w.readableVisible(readable))
	res := w.blocks.SearchCompiled(re, f.trigramOptions(within))
	out := RegexResults{Candidates: res.Candidates}
	for _, h := range res.Hits {
		ref := w.blockAt[h.Doc]
		b, _ := w.Block(ref)
		out.Matches = append(out.Matches, RegexMatch{Ref: ref, Ordinal: b.Ordinal, Spans: h.Matches})
	}
	slices.SortFunc(out.Matches, func(a, b RegexMatch) int {
		if c := strings.Compare(a.Ref.Note, b.Ref.Note); c != 0 {
			return c
		}
		return a.Ordinal - b.Ordinal
	})
	return out
}

// RenderRegex turns a matched block into a hit. Each match counts as its
// own term for dp.BestSnippet, so the snippet shows the densest cluster of
// matches; the location is the first match inside it. Regex hits all score
// 1: a pattern matches or it does not.
func (w *Workspace) RenderRegex(m RegexMatch) Hit {
	block, _ := w.Block(m.Ref)
	matches := make([]dp.Match, len(m.Spans))
	for i, s := range m.Spans {
		matches[i] = dp.Match{Term: i, Start: s.Start, End: s.End}
	}
	s, _ := trigram.MakeSnippet(block.Text, matches, SnippetWindow)
	return Hit{Note: m.Ref.Note, NoteTitle: w.notes[m.Ref.Note].Title, Block: block.ID, Location: s.Focus,
		Snippet: s.Text, Highlights: s.Highlights, Score: 1, Language: codeLanguage(block)}
}
