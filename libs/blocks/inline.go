package blocks

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// NodeKind says what an inline node is. The names are used in conformance
// fixtures.
type NodeKind uint8

// The inline node kinds.
const (
	// NodeText is literal text, including delimiters that matched nothing.
	NodeText NodeKind = 1
	// NodeEscape is a backslash and the ASCII punctuation character it
	// makes literal.
	NodeEscape NodeKind = 2
	// NodeCode is a code span; its content is raw (nothing inside is parsed).
	NodeCode NodeKind = 3
	// NodeEmphasis is *text* or _text_.
	NodeEmphasis NodeKind = 4
	// NodeStrong is **text** or __text__.
	NodeStrong NodeKind = 5
	// NodeLink is [label](url).
	NodeLink NodeKind = 6
	// NodeWikiLink is [[target]], a link to the note titled target.
	NodeWikiLink NodeKind = 7
	// NodeMention is @handle.
	NodeMention NodeKind = 8
)

var nodeKindNames = [...]string{
	NodeText: "text", NodeEscape: "escape", NodeCode: "code", NodeEmphasis: "emphasis",
	NodeStrong: "strong", NodeLink: "link", NodeWikiLink: "wikilink", NodeMention: "mention",
}

// String returns the kind's fixture name.
func (k NodeKind) String() string {
	if k >= NodeText && k <= NodeMention {
		return nodeKindNames[k]
	}
	return fmt.Sprintf("NodeKind(%d)", uint8(k))
}

// Node is one inline node of a block's text. Positions count code points of
// the parsed text (ADR-0003) and are half-open.
//
// [Start, End) is the whole construct, delimiters included, and
// [ContentStart, ContentEnd) its inside: the text itself for NodeText, the
// escaped character, the code, the label of a link, the target of a wiki
// link, the handle of a mention (after the @). Children are set for
// emphasis, strong and links only, and cover the content exactly.
//
// Value is the content as a string for leaf nodes, and the URL for links.
//
// Rust: an enum with one variant per kind, each holding its span and only
// the fields it uses.
type Node struct {
	Kind         NodeKind
	Start, End   int
	ContentStart int
	ContentEnd   int
	Value        string
	Children     []Node
}

// ParseInline parses a block's inline Markdown (docs/spec/blocks.md section
// 5). It never fails: anything that does not form a construct is text, so
// unmatched delimiters render as themselves. The returned nodes cover the
// whole text; concatenating their source spans gives the text back.
//
// Invalid UTF-8 is read as U+FFFD per bad byte, as Go's range loop does;
// positions then count the decoded code points.
//
// Time O(n).
func ParseInline(text string) []Node {
	src := []rune(text)
	return parseRange(src, 0, len(src), true)
}

// delim is a run of '*' or '_' that may open or close emphasis. [lo, hi) is
// the part not yet consumed: closing consumes from the left, opening from the
// right, so matches nest.
type delim struct {
	char       rune
	start, end int
	lo, hi     int
	canOpen    bool
	canClose   bool
	active     bool
	prev, next int // neighbours in the same character's active list, -1 for none
}

type emphasisMatch struct {
	kind                                 NodeKind
	start, contentStart, contentEnd, end int
}

// parseRange parses src[lo:hi]. Links and wiki links are disabled inside a
// link's label, where brackets cannot occur anyway, but the flag keeps the
// recursion obviously finite.
func parseRange(src []rune, lo, hi int, allowLinks bool) []Node {
	var atoms []Node // leaf nodes and text, in order, without delimiter runs
	var delims []delim
	text := func(start, end int) {
		if start < end {
			atoms = append(atoms, textNode(start, end))
		}
	}
	textStart := lo
	var code *backtickRuns
	for i := lo; i < hi; {
		c := src[i]
		var leaf Node
		var ok bool
		switch {
		case c == '\\' && i+1 < hi && isASCIIPunct(src[i+1]):
			leaf, ok = Node{Kind: NodeEscape, Start: i, End: i + 2, ContentStart: i + 1, ContentEnd: i + 2, Value: string(src[i+1])}, true
		case c == '`':
			if code == nil {
				code = indexBacktickRuns(src, lo, hi)
			}
			leaf, ok = code.span(src, i, hi)
			if !ok {
				// The whole run is literal; skipping it keeps a shorter run
				// inside it from being tried as an opener.
				i += runLength(src, i, hi, '`')
				continue
			}
		case c == '[' && allowLinks:
			if leaf, ok = wikiLink(src, i, hi); !ok {
				leaf, ok = link(src, i, hi)
			}
		case c == '@':
			leaf, ok = mention(src, i, hi)
		case c == '*' || c == '_':
			text(textStart, i)
			n := runLength(src, i, hi, c)
			d := delim{char: c, start: i, end: i + n, lo: i, hi: i + n, active: true, prev: -1, next: -1}
			d.canOpen, d.canClose = flanking(src, lo, hi, i, i+n, c)
			delims = append(delims, d)
			i += n
			textStart = i
			continue
		}
		if !ok {
			i++
			continue
		}
		text(textStart, i)
		atoms = append(atoms, leaf)
		i = leaf.End
		textStart = i
	}
	text(textStart, hi)
	matches := matchEmphasis(delims)
	for _, d := range delims {
		text(d.lo, d.hi)
	}
	slices.SortFunc(atoms, func(a, b Node) int { return a.Start - b.Start })
	slices.SortFunc(matches, func(a, b emphasisMatch) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return b.end - a.end
	})
	b := treeBuilder{src: src, atoms: atoms, matches: matches}
	return b.build(lo, hi)
}

// matchEmphasis pairs delimiter runs, following CommonMark's "process
// emphasis" without the rule of three: for each run that can close, in
// order, the nearest earlier active run of the same character that can open
// is its opener; two characters are used from each when both have at least
// two left (strong), otherwise one (emphasis). Runs between a matched pair
// become text inside it. Each character has its own list of active runs, and
// a run that can close but finds no opener and cannot open is dropped, so
// every active run before a closer can open and the nearest one is taken in
// O(1): matching is linear.
func matchEmphasis(ds []delim) []emphasisMatch {
	var matches []emphasisMatch
	tail := map[rune]int{'*': -1, '_': -1}
	remove := func(i int) {
		d := &ds[i]
		d.active = false
		if d.prev >= 0 {
			ds[d.prev].next = d.next
		}
		if d.next >= 0 {
			ds[d.next].prev = d.prev
		} else {
			tail[d.char] = d.prev
		}
	}
	for ci := range ds {
		closer := &ds[ci]
		for closer.canClose && closer.hi > closer.lo {
			oi := tail[closer.char]
			if oi < 0 {
				break
			}
			opener := &ds[oi]
			use := 1
			if opener.hi-opener.lo >= 2 && closer.hi-closer.lo >= 2 {
				use = 2
			}
			kind := NodeEmphasis
			if use == 2 {
				kind = NodeStrong
			}
			matches = append(matches, emphasisMatch{kind: kind, start: opener.hi - use, contentStart: opener.hi, contentEnd: closer.lo, end: closer.lo + use})
			opener.hi -= use
			closer.lo += use
			// Runs of the other character between the pair can no longer
			// match anything outside it.
			other := '*'
			if closer.char == '*' {
				other = '_'
			}
			for j := tail[other]; j >= 0 && ds[j].start > opener.start; j = tail[other] {
				remove(j)
			}
			if opener.hi == opener.lo {
				remove(oi)
			}
		}
		if closer.hi > closer.lo && closer.canOpen {
			closer.prev = tail[closer.char]
			if closer.prev >= 0 {
				ds[closer.prev].next = ci
			}
			tail[closer.char] = ci
		} else {
			closer.active = false
		}
	}
	return matches
}

type treeBuilder struct {
	src     []rune
	atoms   []Node
	matches []emphasisMatch
	ai, mi  int
}

// build emits the nodes covering [lo, hi): emphasis matches as containers,
// atoms as they are, with adjacent text merged.
func (b *treeBuilder) build(lo, hi int) []Node {
	var out []Node
	for pos := lo; pos < hi; {
		var n Node
		if b.mi < len(b.matches) && b.matches[b.mi].start == pos {
			m := b.matches[b.mi]
			b.mi++
			n = Node{Kind: m.kind, Start: m.start, End: m.end, ContentStart: m.contentStart, ContentEnd: m.contentEnd}
			n.Children = b.build(m.contentStart, m.contentEnd)
		} else {
			n = b.atoms[b.ai]
			b.ai++
		}
		if last := len(out) - 1; n.Kind == NodeText && last >= 0 && out[last].Kind == NodeText {
			// Extend now and slice the source once below: re-slicing on every
			// merge would make long runs of unmatched delimiters quadratic.
			out[last].End, out[last].ContentEnd = n.End, n.End
		} else {
			out = append(out, n)
		}
		pos = n.End
	}
	for i := range out {
		if out[i].Kind == NodeText {
			out[i].Value = string(b.src[out[i].Start:out[i].End])
		}
	}
	return out
}

// textNode returns a text node without its Value, which build fills in once
// adjacent text has been merged.
func textNode(start, end int) Node {
	return Node{Kind: NodeText, Start: start, End: end, ContentStart: start, ContentEnd: end}
}

func runLength(src []rune, i, hi int, c rune) int {
	n := 0
	for i+n < hi && src[i+n] == c {
		n++
	}
	return n
}

// backtickRuns indexes the maximal backtick runs of a range by length, so
// finding the closer of a code span is a lookup instead of a scan. Openers
// are tried left to right, so each length's cursor only moves forward and
// the whole range costs O(n).
type backtickRuns struct {
	starts map[int][]int // run length -> run start positions, ascending
	next   map[int]int   // run length -> index of the first start not yet passed
}

func indexBacktickRuns(src []rune, lo, hi int) *backtickRuns {
	idx := &backtickRuns{starts: map[int][]int{}, next: map[int]int{}}
	for j := lo; j < hi; {
		if src[j] != '`' {
			j++
			continue
		}
		m := runLength(src, j, hi, '`')
		idx.starts[m] = append(idx.starts[m], j)
		j += m
	}
	return idx
}

// span reads a code span opening at i: the n backticks from i, closed by the
// next maximal run of exactly n backticks after them.
func (idx *backtickRuns) span(src []rune, i, hi int) (Node, bool) {
	n := runLength(src, i, hi, '`')
	starts := idx.starts[n]
	k := idx.next[n]
	for k < len(starts) && starts[k] < i+n {
		k++
	}
	idx.next[n] = k
	if k == len(starts) {
		return Node{}, false
	}
	j := starts[k]
	return Node{Kind: NodeCode, Start: i, End: j + n, ContentStart: i + n, ContentEnd: j, Value: string(src[i+n : j])}, true
}

// wikiLink reads [[target]] at i, where target is one or more code points
// other than '[', ']' and newline: the same rule as the seed generator's and
// the search service's link pattern \[\[([^\[\]\n]+)\]\].
func wikiLink(src []rune, i, hi int) (Node, bool) {
	if i+1 >= hi || src[i+1] != '[' {
		return Node{}, false
	}
	j := i + 2
	for j < hi && src[j] != '[' && src[j] != ']' && src[j] != '\n' {
		j++
	}
	if j == i+2 || j+1 >= hi || src[j] != ']' || src[j+1] != ']' {
		return Node{}, false
	}
	return Node{Kind: NodeWikiLink, Start: i, End: j + 2, ContentStart: i + 2, ContentEnd: j, Value: string(src[i+2 : j])}, true
}

// link reads [label](url) at i. The label has no brackets or newlines; the
// URL has no white space or parentheses and may be empty.
func link(src []rune, i, hi int) (Node, bool) {
	j := i + 1
	for j < hi && src[j] != '[' && src[j] != ']' && src[j] != '\n' {
		j++
	}
	if j+1 >= hi || src[j] != ']' || src[j+1] != '(' {
		return Node{}, false
	}
	k := j + 2
	for k < hi && src[k] != ')' && src[k] != '(' && !unicode.IsSpace(src[k]) {
		k++
	}
	if k >= hi || src[k] != ')' {
		return Node{}, false
	}
	n := Node{Kind: NodeLink, Start: i, End: k + 1, ContentStart: i + 1, ContentEnd: j, Value: string(src[j+2 : k])}
	n.Children = parseRange(src, i+1, j, false)
	return n, true
}

// mention reads @handle at i. The @ must not follow a letter, digit, mark or
// one of "_.@-+" (so e-mail addresses are not mentions); the handle is an
// ASCII letter, digit or '_' followed by ASCII letters, digits, '_', '.' and
// '-', without trailing '.' or '-' (so "@mira." at the end of a sentence
// mentions "mira").
func mention(src []rune, i, hi int) (Node, bool) {
	if i > 0 {
		p := src[i-1]
		if unicode.IsLetter(p) || unicode.IsDigit(p) || unicode.IsMark(p) || strings.ContainsRune("_.@-+", p) {
			return Node{}, false
		}
	}
	j := i + 1
	if j >= hi || !isHandleStart(src[j]) {
		return Node{}, false
	}
	for j < hi && (isHandleStart(src[j]) || src[j] == '.' || src[j] == '-') {
		j++
	}
	for src[j-1] == '.' || src[j-1] == '-' {
		j--
	}
	return Node{Kind: NodeMention, Start: i, End: j, ContentStart: i + 1, ContentEnd: j, Value: string(src[i+1 : j])}, true
}

func isHandleStart(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// flanking decides whether the delimiter run src[i:j] of c can open and
// close emphasis, by CommonMark's left- and right-flanking rules. The ends of
// the parsed range count as white space.
func flanking(src []rune, lo, hi, i, j int, c rune) (canOpen, canClose bool) {
	before, after := ' ', ' '
	if i > lo {
		before = src[i-1]
	}
	if j < hi {
		after = src[j]
	}
	left := !unicode.IsSpace(after) && (!isPunct(after) || unicode.IsSpace(before) || isPunct(before))
	right := !unicode.IsSpace(before) && (!isPunct(before) || unicode.IsSpace(after) || isPunct(after))
	if c == '*' {
		return left, right
	}
	// '_' does not open or close inside a word, so snake_case_names stay
	// plain.
	return left && (!right || isPunct(before)), right && (!left || isPunct(after))
}

func isPunct(r rune) bool { return unicode.IsPunct(r) || unicode.IsSymbol(r) }

func isASCIIPunct(r rune) bool {
	return r < 0x80 && strings.ContainsRune("!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", r)
}

// PlainText renders nodes as the text a reader sees: delimiters removed,
// escapes resolved, wiki links as their target, mentions with their @.
func PlainText(nodes []Node) string {
	var b strings.Builder
	var walk func([]Node)
	walk = func(ns []Node) {
		for _, n := range ns {
			switch n.Kind {
			case NodeEmphasis, NodeStrong, NodeLink:
				walk(n.Children)
			case NodeMention:
				b.WriteString("@" + n.Value)
			default:
				b.WriteString(n.Value)
			}
		}
	}
	walk(nodes)
	return b.String()
}

// CheckTree verifies the invariants of a parse of text (docs/spec/blocks.md
// section 5):
//
//   - P1 the top-level nodes cover [0, len) contiguously, and each
//     container's children cover its content contiguously;
//   - P2 every node has Start <= ContentStart <= ContentEnd <= End, and
//     non-empty source, and no two adjacent siblings are both text;
//   - P3 each kind's delimiters are what the kind requires, and Value
//     matches the source.
func CheckTree(text string, nodes []Node) error {
	src := []rune(text)
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvariant, fmt.Sprintf(format, args...))
	}
	var check func(ns []Node, lo, hi int) error
	check = func(ns []Node, lo, hi int) error {
		pos := lo
		for i, n := range ns {
			if n.Start != pos {
				return fail("P1: node at %d, expected %d", n.Start, pos)
			}
			if !(n.Start <= n.ContentStart && n.ContentStart <= n.ContentEnd && n.ContentEnd <= n.End) || n.Start == n.End || n.End > hi {
				return fail("P2: node %s has span %d %d %d %d", n.Kind, n.Start, n.ContentStart, n.ContentEnd, n.End)
			}
			if i > 0 && n.Kind == NodeText && ns[i-1].Kind == NodeText {
				return fail("P2: adjacent text nodes at %d", n.Start)
			}
			if err := checkDelimiters(src, n); err != nil {
				return fail("P3: %v", err)
			}
			if n.Children != nil || n.Kind == NodeEmphasis || n.Kind == NodeStrong || n.Kind == NodeLink {
				if n.Kind != NodeEmphasis && n.Kind != NodeStrong && n.Kind != NodeLink {
					return fail("P2: %s node has children", n.Kind)
				}
				if err := check(n.Children, n.ContentStart, n.ContentEnd); err != nil {
					return err
				}
			}
			pos = n.End
		}
		if pos != hi {
			return fail("P1: nodes end at %d, expected %d", pos, hi)
		}
		return nil
	}
	return check(nodes, 0, len(src))
}

func checkDelimiters(src []rune, n Node) error {
	open, close := string(src[n.Start:n.ContentStart]), string(src[n.ContentEnd:n.End])
	content := string(src[n.ContentStart:n.ContentEnd])
	switch n.Kind {
	case NodeText:
		if open != "" || close != "" || n.Value != content {
			return fmt.Errorf("text node %d has delimiters or wrong value", n.Start)
		}
	case NodeEscape:
		if open != `\` || close != "" || len(content) != 1 || !isASCIIPunct(rune(content[0])) || n.Value != content {
			return fmt.Errorf("escape %d malformed", n.Start)
		}
	case NodeCode:
		if open == "" || strings.Trim(open, "`") != "" || open != close || n.Value != content {
			return fmt.Errorf("code span %d has delimiters %q and %q", n.Start, open, close)
		}
	case NodeEmphasis, NodeStrong:
		want := 1
		if n.Kind == NodeStrong {
			want = 2
		}
		if len(open) != want || open != close || strings.Trim(open, string(open[0])) != "" || (open[0] != '*' && open[0] != '_') || n.Value != "" {
			return fmt.Errorf("%s %d has delimiters %q and %q", n.Kind, n.Start, open, close)
		}
	case NodeLink:
		if open != "[" || close != "]("+n.Value+")" {
			return fmt.Errorf("link %d has delimiters %q and %q", n.Start, open, close)
		}
	case NodeWikiLink:
		if open != "[[" || close != "]]" || n.Value != content || content == "" {
			return fmt.Errorf("wiki link %d malformed", n.Start)
		}
	case NodeMention:
		if open != "@" || close != "" || n.Value != content || content == "" {
			return fmt.Errorf("mention %d malformed", n.Start)
		}
	default:
		return fmt.Errorf("unknown kind %d", n.Kind)
	}
	return nil
}
