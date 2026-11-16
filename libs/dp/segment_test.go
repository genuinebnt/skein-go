package dp

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

var testFrequencies = map[string]uint64{
	"rust": 400, "async": 300, "a": 900, "as": 200, "sync": 150, "rus": 1,
	"go": 500, "lang": 120, "golang": 80, "web": 250, "socket": 90, "websocket": 60,
	"漢": 50, "字": 40, "漢字": 30, "zero": 0,
}

func newTestSegmenter(t testing.TB) *Segmenter {
	t.Helper()
	s, err := NewSegmenter(testFrequencies)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	return s
}

// oracleSegment tries every segmentation of text and returns the best by
// the rules on Segment: lowest cost, then fewest words, then longest first
// word (lexicographically largest list of word ends).
func oracleSegment(s *Segmenter, text string) Segmentation {
	runes := []rune(text)
	n := len(runes)
	if n == 0 {
		return Segmentation{}
	}
	var best Segmentation
	var bestEnds []int
	for mask := 0; mask < 1<<(n-1); mask++ {
		var words []string
		var ends []int
		var cost int64
		start := 0
		for i := 1; i <= n; i++ {
			if i == n || mask&(1<<(i-1)) != 0 {
				w := string(runes[start:i])
				words = append(words, w)
				ends = append(ends, i)
				cost += s.WordCost(w)
				start = i
			}
		}
		better := bestEnds == nil || cost < best.Cost ||
			(cost == best.Cost && len(words) < len(best.Words)) ||
			(cost == best.Cost && len(words) == len(best.Words) && slices.Compare(ends, bestEnds) > 0)
		if better {
			best, bestEnds = Segmentation{Words: words, Cost: cost}, ends
		}
	}
	return best
}

func TestSegmentMatchesAllSegmentationsOracle(t *testing.T) {
	s := newTestSegmenter(t)
	r := newRand(testSeed(t))
	pieces := []string{"rust", "async", "a", "s", "go", "lang", "web", "socket", "x", "q", "漢", "字"}
	for trial := 0; trial < 500; trial++ {
		var b strings.Builder
		for b.Len() == 0 || (len([]rune(b.String())) < 12 && r.IntN(3) > 0) {
			b.WriteString(pieces[r.IntN(len(pieces))])
		}
		text := string([]rune(b.String())[:min(12, len([]rune(b.String())))])
		got := s.Segment(text)
		want := oracleSegment(s, text)
		if got.Cost != want.Cost || !slices.Equal(got.Words, want.Words) {
			t.Fatalf("Segment(%q) = %+v, oracle %+v", text, got, want)
		}
		if strings.Join(got.Words, "") != text {
			t.Fatalf("Segment(%q) words %q do not join back", text, got.Words)
		}
	}
}

func TestSegmentSplitsRunTogetherQueries(t *testing.T) {
	s := newTestSegmenter(t)
	cases := map[string][]string{
		"rustasync":    {"rust", "async"},
		"golang":       {"golang"},
		"websocket":    {"websocket"},
		"rustqzxasync": {"rust", "qzx", "async"},
		"漢字":           {"漢字"},
		"":             nil,
	}
	for text, want := range cases {
		if got := s.Segment(text); !slices.Equal(got.Words, want) {
			t.Errorf("Segment(%q) = %q, want %q", text, got.Words, want)
		}
	}
}

func TestSegmentKeepsInvalidUTF8Bytes(t *testing.T) {
	s := newTestSegmenter(t)
	text := "rust\xffasync"
	if got := s.Segment(text); strings.Join(got.Words, "") != text {
		t.Fatalf("Segment(%q) = %q", text, got.Words)
	}
}

func TestNewSegmenterRejectsBadFrequencies(t *testing.T) {
	if _, err := NewSegmenter(map[string]uint64{"": 3}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty word: err = %v", err)
	}
	if _, err := NewSegmenter(map[string]uint64{"a": 1 << 63, "b": 1 << 63}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("overflow: err = %v", err)
	}
	s, err := NewSegmenter(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Segment("abc"); !slices.Equal(got.Words, []string{"abc"}) {
		t.Errorf("empty dictionary: %q", got.Words)
	}
}
