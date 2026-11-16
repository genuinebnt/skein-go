package domain

import (
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/libs/dp"
	"github.com/genuinebnt/skein-go/libs/trigram"
)

// TitleSuggestion is one completion for a typed prefix.
type TitleSuggestion struct {
	Note     string
	Title    string
	Distance int
}

// PrefixDistance is the Damerau–Levenshtein distance from prefix to the
// closest prefix of title, both lowercased: 0 when title starts with
// prefix. Only title prefixes within maxEdits code points of the typed
// length can be within maxEdits edits, so only those are compared.
func PrefixDistance(prefix, title string, maxEdits int) int {
	p := strings.ToLower(prefix)
	t := []rune(strings.ToLower(title))
	if strings.HasPrefix(string(t), p) {
		return 0
	}
	n := utf8.RuneCountInString(p)
	best := maxEdits + 1
	for j := max(0, n-maxEdits); j <= min(len(t), n+maxEdits); j++ {
		best = min(best, dp.DamerauLevenshtein(p, string(t[:j])))
	}
	return best
}

// SuggestTitles completes a typed prefix to readable note titles,
// tolerating typos: every title starting with the prefix (distance 0),
// then titles whose start is within the prefix's typo budget
// (TypoBudget), ordered by distance, then title, then note ID.
//
// Candidates come from the title trigram index: a title within k edits of
// the prefix contains a substring within k edits of it, so the q-gram
// count filter (FuzzyCandidates) keeps every such title. The exact prefix
// distance then decides. Damerau distance is a metric, so a BK-tree over
// fixed-length title prefixes would also work; the trigram filter needs no
// second structure and already exists for search.
func (w *Workspace) SuggestTitles(prefix string, readable Readable, limit int) []TitleSuggestion {
	if strings.TrimSpace(prefix) == "" || limit <= 0 {
		return nil
	}
	within := &trigram.Bitmap{}
	for _, note := range w.readableVisible(readable) {
		within.Add(w.titleDoc[note])
	}
	budget := TypoBudget(prefix)
	candidates, err := w.titles.FuzzyCandidates(prefix, budget, trigram.Options{Within: within})
	if err != nil {
		return nil
	}
	var out []TitleSuggestion
	candidates.ForEach(func(id uint32) bool {
		note := w.titleAt[id]
		title := w.notes[note].Title
		if d := PrefixDistance(prefix, title, budget); d <= budget {
			out = append(out, TitleSuggestion{Note: note, Title: title, Distance: d})
		}
		return true
	})
	slices.SortFunc(out, func(a, b TitleSuggestion) int {
		if a.Distance != b.Distance {
			return a.Distance - b.Distance
		}
		if c := strings.Compare(a.Title, b.Title); c != 0 {
			return c
		}
		return strings.Compare(a.Note, b.Note)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
