package dp

import (
	"fmt"
	"math"
	"slices"
	"unicode/utf8"
)

// costScale converts natural-log costs to integers (micro-nats). Integer
// costs make sums exact and ties well defined, so Go and Rust agree even
// where floating-point sums would round differently.
const costScale = 1e6

// Segmenter splits text written without spaces into words ("rustasync" to
// "rust async"), for queries typed without spaces and for identifiers. Build
// one per dictionary with NewSegmenter; it is read-only afterwards and safe
// to share.
type Segmenter struct {
	cost           map[string]int64 // cost of each known word
	longestKnown   int              // in code points
	unknownBase    int64            // cost of a one-code-point unknown word
	unknownPerRune int64            // extra cost per further code point
}

// NewSegmenter builds a segmenter from word frequencies (occurrence counts
// from a corpus). Words with a count of zero are ignored. Keys should be in
// the form queries will be in (the search service lowercases both).
//
// The model is a unigram language model, as in Peter Norvig's "Natural
// Language Corpus Data" (2009): P(w) = count(w) / N for a known word, where
// N is the sum of all counts, and P(w) = 10 / (U · 10^len(w)) for an unknown
// word of len(w) code points, with U = max(N, 10). A word's cost is
// -ln P(w), in micro-nats, rounded to an integer:
//
//	known:   round(1e6 · (ln N - ln count(w)))
//	unknown: round(1e6 · ln U) + (len(w) - 1) · round(1e6 · ln 10)
//
// An unknown word always costs at least as much as any known word (count <=
// N <= U). Cutting an unknown run into k pieces costs (k-1)·(ln U - ln 10)
// more than leaving it whole, never less (U is at least 10 for exactly this
// reason, so a tiny or empty dictionary does not split unknown text into
// single characters), so unknown runs stay together: "rustqzxasync" becomes
// "rust qzx async".
func NewSegmenter(frequencies map[string]uint64) (*Segmenter, error) {
	var total uint64
	for word, count := range frequencies {
		if word == "" && count > 0 {
			return nil, fmt.Errorf("%w: the empty string has a frequency", ErrInvalidInput)
		}
		if total+count < total {
			return nil, fmt.Errorf("%w: frequencies overflow uint64", ErrInvalidInput)
		}
		total += count
	}
	s := &Segmenter{cost: make(map[string]int64, len(frequencies))}
	if total == 0 {
		total = 1
	}
	lnTotal := math.Log(float64(total))
	for word, count := range frequencies {
		if count == 0 {
			continue
		}
		s.cost[word] = int64(math.Round(costScale * (lnTotal - math.Log(float64(count)))))
		s.longestKnown = max(s.longestKnown, utf8.RuneCountInString(word))
	}
	s.unknownBase = int64(math.Round(costScale * math.Log(float64(max(total, 10)))))
	s.unknownPerRune = int64(math.Round(costScale * math.Ln10))
	return s, nil
}

// Segmentation is the result of Segment.
type Segmentation struct {
	Words []string
	// Cost is the total cost in micro-nats: the negative log probability of
	// the segmentation under the unigram model, times one million.
	Cost int64
}

// WordCost returns the cost of word under the model, in micro-nats.
func (s *Segmenter) WordCost(word string) int64 {
	if c, ok := s.cost[word]; ok {
		return c
	}
	return s.unknownBase + int64(utf8.RuneCountInString(word)-1)*s.unknownPerRune
}

// Segment splits text into the most probable sequence of words under the
// model: the one with the lowest total cost (Viterbi over word boundaries).
// Ties go to fewer words, then to the longer first word. The words
// concatenate back to text exactly; an empty text gives no words.
//
// Recurrence over suffixes of the n code points:
//
//	best(n) = 0
//	best(i) = min over j > i of WordCost(text[i:j]) + best(j)
//
// Spans longer than the longest known word are unknown and cost a function
// of their length alone, so no lookup is needed for them, but every length
// is still a candidate. Time O(n²) (O(n·L) dictionary lookups for longest
// known word length L), memory O(n). Segment is meant for single query
// tokens; split on white space first.
func (s *Segmenter) Segment(text string) Segmentation {
	// Byte offsets of each code point, so words are slices of text and
	// invalid UTF-8 survives unchanged.
	offsets := make([]int, 0, utf8.RuneCountInString(text)+1)
	for i := range text {
		offsets = append(offsets, i)
	}
	offsets = append(offsets, len(text))
	n := len(offsets) - 1

	cost := make([]int64, n+1)
	words := make([]int, n+1)
	end := make([]int, n+1)
	for i := n - 1; i >= 0; i-- {
		cost[i] = -1
		for j := i + 1; j <= n; j++ {
			var wordCost int64
			if j-i <= s.longestKnown {
				wordCost = s.WordCost(text[offsets[i]:offsets[j]])
			} else {
				wordCost = s.unknownBase + int64(j-i-1)*s.unknownPerRune
			}
			c := wordCost + cost[j]
			if cost[i] < 0 || c < cost[i] || (c == cost[i] && words[j]+1 <= words[i]) {
				cost[i], words[i], end[i] = c, words[j]+1, j
			}
		}
	}
	out := Segmentation{Cost: cost[0]}
	if n == 0 {
		out.Cost = 0
		return out
	}
	for i := 0; i < n; i = end[i] {
		out.Words = append(out.Words, text[offsets[i]:offsets[end[i]]])
	}
	return out
}

// CheckInvariants verifies the segmenter's model: every known word is
// non-empty and costs between zero and the cost of a one-code-point unknown
// word, and the longest-known-word length matches the dictionary. Errors wrap
// ErrInvariant.
func (s *Segmenter) CheckInvariants() error {
	longest := 0
	words := make([]string, 0, len(s.cost))
	for w := range s.cost {
		words = append(words, w)
	}
	slices.Sort(words) // stable error messages
	for _, w := range words {
		c := s.cost[w]
		if w == "" || c < 0 || c > s.unknownBase {
			return fmt.Errorf("%w: word %q costs %d, unknown base is %d", ErrInvariant, w, c, s.unknownBase)
		}
		longest = max(longest, utf8.RuneCountInString(w))
	}
	if longest != s.longestKnown {
		return fmt.Errorf("%w: longest known word has %d code points, recorded %d", ErrInvariant, longest, s.longestKnown)
	}
	if s.unknownPerRune <= 0 {
		return fmt.Errorf("%w: unknown words cost %d per extra code point", ErrInvariant, s.unknownPerRune)
	}
	return nil
}
