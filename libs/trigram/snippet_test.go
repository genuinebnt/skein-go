package trigram

import (
	"slices"
	"strings"
	"testing"

	"github.com/genuinebnt/skein-go/libs/dp"
)

func TestSnippetCentresTheBestSpanAndClipsHighlights(t *testing.T) {
	text := strings.Repeat("x", 50) + "raft log" + strings.Repeat("y", 50)
	matches := []dp.Match{{Term: 0, Start: 50, End: 54}, {Term: 1, Start: 55, End: 58}}
	s, err := MakeSnippet(text, matches, 20)
	if err != nil {
		t.Fatal(err)
	}
	// The span 50..58 is 8 long; 12 spare code points go 6 to each side.
	if s.Window != (Span{44, 64}) || s.Text != "xxxxxxraft logyyyyyy" {
		t.Fatalf("window %v %q", s.Window, s.Text)
	}
	if !slices.Equal(s.Highlights, []Span{{6, 10}, {11, 14}}) || s.Focus != (Span{50, 54}) {
		t.Fatalf("highlights %v focus %v", s.Highlights, s.Focus)
	}
}

func TestSnippetStaysInsideTheTextAndCountsCodePoints(t *testing.T) {
	text := "é🙂 abc"
	s, _ := MakeSnippet(text, []dp.Match{{Start: 3, End: 6}}, 100)
	if s.Text != text || s.Window != (Span{0, 6}) || !slices.Equal(s.Highlights, []Span{{3, 6}}) {
		t.Fatalf("%+v", s)
	}
	s, _ = MakeSnippet(text, nil, 3)
	if s.Text != "é🙂 " || len(s.Highlights) != 0 {
		t.Fatalf("no-match snippet %+v", s)
	}
}

func TestSnippetOfAMatchLongerThanTheWindowStartsAtTheMatch(t *testing.T) {
	text := "0123456789abcdefghij"
	s, _ := MakeSnippet(text, []dp.Match{{Start: 5, End: 18}}, 4)
	if s.Text != "3456" || s.Window.Start != 3 {
		t.Fatalf("%+v", s)
	}
	if !slices.Equal(s.Highlights, []Span{{2, 4}}) || s.Focus != (Span{5, 18}) {
		t.Fatalf("highlights %v focus %v", s.Highlights, s.Focus)
	}
}

func TestSnippetRejectsMatchesOutsideTheText(t *testing.T) {
	if _, err := MakeSnippet("abc", []dp.Match{{Start: 2, End: 5}}, 10); err == nil {
		t.Fatal("match past the end accepted")
	}
	if _, err := MakeSnippet("abc", nil, -1); err == nil {
		t.Fatal("negative window accepted")
	}
}
