package blocks

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// shape renders a parse compactly for table tests: kind(value){children}.
func shape(ns []Node) string {
	var parts []string
	for _, n := range ns {
		s := n.Kind.String()
		if n.Value != "" {
			s += fmt.Sprintf("(%s)", n.Value)
		}
		if len(n.Children) > 0 {
			s += "{" + shape(n.Children) + "}"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func mustParse(t testing.TB, text string) []Node {
	t.Helper()
	nodes := ParseInline(text)
	if err := CheckTree(text, nodes); err != nil {
		t.Fatalf("%q: %v\n%s", text, err, shape(nodes))
	}
	return nodes
}

func TestParseInlineExamples(t *testing.T) {
	cases := []struct{ in, want, plain string }{
		{"plain", "text(plain)", "plain"},
		{"**bold** and *it*", "strong{text(bold)} text( and ) emphasis{text(it)}", "bold and it"},
		{"***both***", "emphasis{strong{text(both)}}", "both"},
		{"**unclosed", "text(**unclosed)", "**unclosed"},
		{"a * b * c", "text(a * b * c)", "a * b * c"},
		{"snake_case_name", "text(snake_case_name)", "snake_case_name"},
		{"_it_ and __st__", "emphasis{text(it)} text( and ) strong{text(st)}", "it and st"},
		{"`code *not* parsed`", "code(code *not* parsed)", "code *not* parsed"},
		{"``a`b``", "code(a`b)", "a`b"},
		{"`unclosed code", "text(`unclosed code)", "`unclosed code"},
		{"see [[Raft overview]] now", "text(see ) wikilink(Raft overview) text( now)", "see Raft overview now"},
		{"[[]] and [[a\nb]]", "text([[]] and [[a\nb]])", "[[]] and [[a\nb]]"},
		{"[[[nested]]]", "text([) wikilink(nested) text(])", "[nested]"},
		{"[docs](https://x.example/a) end", "link(https://x.example/a){text(docs)} text( end)", "docs end"},
		{"[**bold** label](u)", "link(u){strong{text(bold)} text( label)}", "bold label"},
		{"[no url] (x)", "text([no url] (x))", "[no url] (x)"},
		{"ping @mira.", "text(ping ) mention(mira) text(.)", "ping @mira."},
		{"mail a@b.example", "text(mail a@b.example)", "mail a@b.example"},
		{"@valeria.balogun, hi", "mention(valeria.balogun) text(, hi)", "@valeria.balogun, hi"},
		{`\*not emphasis\*`, "escape(*) text(not emphasis) escape(*)", "*not emphasis*"},
		{`a\b`, `text(a\b)`, `a\b`},
		{"*a **b** c*", "emphasis{text(a ) strong{text(b)} text( c)}", "a b c"},
		{"**a *b** c*", "emphasis{emphasis{text(a ) emphasis{text(b)}} text( c)}", "a b c"},
		{"*🙂*", "emphasis{text(🙂)}", "🙂"},
	}
	for _, c := range cases {
		nodes := mustParse(t, c.in)
		if got := shape(nodes); got != c.want {
			t.Errorf("%q\n got %s\nwant %s", c.in, got, c.want)
		}
		if got := PlainText(nodes); got != c.plain {
			t.Errorf("%q: plain %q, want %q", c.in, got, c.plain)
		}
	}
}

func TestSpansCountCodePoints(t *testing.T) {
	nodes := mustParse(t, "漢🙂 **x**")
	if n := nodes[1]; n.Kind != NodeStrong || n.Start != 3 || n.End != 8 || n.ContentStart != 5 {
		t.Fatalf("strong span %+v, want start 3 end 8 content 5 (code points, not bytes)", n)
	}
}

// delimiterHeavy is an alphabet that makes every construct, and every way
// of almost forming one, likely.
var delimiterHeavy = []rune("ab *_`[]()@.\\\n漢-")

func TestParseInlineInvariantsHoldOnRandomText(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	for i := 0; i < 20000; i++ {
		text := randomRunes(r, delimiterHeavy, 24)
		mustParse(t, text)
	}
}

// The parser's wiki links are the search service's link pattern, applied
// outside code spans and escapes; on text without backticks or backslashes
// the two must find the same targets.
func TestWikiLinksMatchTheLinkPatternOutsideCode(t *testing.T) {
	pattern := regexp.MustCompile(`\[\[([^\[\]\n]+)\]\]`)
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	alphabet := []rune("ab []\n*@")
	for i := 0; i < 20000; i++ {
		text := randomRunes(r, alphabet, 20)
		var want []string
		for _, m := range pattern.FindAllStringSubmatch(text, -1) {
			want = append(want, m[1])
		}
		var got []string
		var walk func([]Node)
		walk = func(ns []Node) {
			for _, n := range ns {
				if n.Kind == NodeWikiLink {
					got = append(got, n.Value)
				}
				walk(n.Children)
			}
		}
		walk(mustParse(t, text))
		if !slices.Equal(got, want) {
			t.Fatalf("%q: parser %q, pattern %q", text, got, want)
		}
	}
}

func TestLinksAndMentionsSkipCode(t *testing.T) {
	bs := []Block{
		{Attrs: DefaultAttrs(), Text: "[[B]] and [[A]] and [[B]], ask @kenta"},
		{Attrs: Attrs{Type: Code}, Text: "[[NotALink]] @nobody"},
		{Attrs: Attrs{Type: Table}, Text: "| [[C]] | `[[D]]` @ottilie |"},
	}
	if got := Links(bs); !slices.Equal(got, []string{"A", "B", "C"}) {
		t.Errorf("Links = %q", got)
	}
	if got := Mentions(bs); !slices.Equal(got, []string{"kenta", "ottilie"}) {
		t.Errorf("Mentions = %q", got)
	}
}

func TestLongDelimiterRunsParseInLinearTime(t *testing.T) {
	// Quadratic matching would take minutes here.
	var growing strings.Builder
	for n := 1; n < 400; n++ {
		growing.WriteString(strings.Repeat("`", n) + " x ")
	}
	for _, text := range []string{
		strings.Repeat("*a _b ", 100_000) + strings.Repeat("`", 3),
		strings.Repeat("` ``", 50_000),
		growing.String(),
	} {
		if err := CheckTree(text, ParseInline(text)); err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzParseInline(f *testing.F) {
	for _, s := range []string{"**a** *b* `c` [[d]] [e](f) @g \\*", "***", "_a_b_", "[[a]]]]", "``` ` ``"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if !utf8.ValidString(text) {
			text = string([]rune(text))
		}
		nodes := ParseInline(text)
		if err := CheckTree(text, nodes); err != nil {
			t.Fatalf("%q: %v", text, err)
		}
		_ = PlainText(nodes)
	})
}
