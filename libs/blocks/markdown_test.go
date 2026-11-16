package blocks

import (
	"bufio"
	"encoding/json"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
)

func blk(t Type, text string) Block { return Block{Attrs: Attrs{Type: t}, Text: text} }

func TestImportReadsEachBlockType(t *testing.T) {
	md := "# Title\n\nPara one\ncontinued\n\n- a\n  - nested\n1. first\n7) second\n- [ ] open\n- [x] done\n\n> quoted\n> more\n\n| a | b |\n|---|---|\n\n```go\nfunc f() {}\n```\n\n---\n\n#### deep\n\n&nbsp;\n\n\\# not a heading"
	want := []Block{
		blk(Heading1, "Title"),
		blk(Paragraph, "Para one continued"),
		blk(Bulleted, "a"),
		{Attrs: Attrs{Type: Bulleted, Indent: 1}, Text: "nested"},
		blk(Numbered, "first"),
		blk(Numbered, "second"),
		blk(Todo, "open"),
		{Attrs: Attrs{Type: Todo, Checked: true}, Text: "done"},
		blk(Quote, "quoted\nmore"),
		blk(Table, "| a | b |\n|---|---|"),
		{Attrs: Attrs{Type: Code, Language: "go"}, Text: "func f() {}"},
		blk(Divider, ""),
		blk(Heading3, "deep"),
		blk(Paragraph, ""),
		blk(Paragraph, "# not a heading"),
	}
	if got := Import(md); !slices.EqualFunc(got, want, blockContentEqual) {
		t.Fatalf("Import:\n got %+v\nwant %+v", got, want)
	}
}

func blockContentEqual(a, b Block) bool {
	return a.Attrs == b.Attrs && a.Text == b.Text
}

func TestExportNumbersListsPerIndent(t *testing.T) {
	bs := []Block{
		blk(Numbered, "a"), blk(Numbered, "b"),
		{Attrs: Attrs{Type: Numbered, Indent: 1}, Text: "b1"},
		{Attrs: Attrs{Type: Numbered, Indent: 1}, Text: "b2"},
		blk(Numbered, "c"),
		blk(Paragraph, "break"),
		blk(Numbered, "again"),
	}
	want := "1. a\n2. b\n  1. b1\n  2. b2\n3. c\n\nbreak\n\n1. again"
	if got := Export(bs); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestExportEscapesParagraphsThatLookLikeOtherBlocks(t *testing.T) {
	for _, text := range []string{"# x", "- x", "1. x", "> x", "| x", "```", "---", "&nbsp;", " ", `\# x`, `\\\- y`, "  - indented"} {
		bs := []Block{blk(Paragraph, text)}
		md := Export(bs)
		if got := Import(md); !slices.EqualFunc(got, bs, blockContentEqual) {
			t.Errorf("paragraph %q exported as %q imports as %+v", text, md, got)
		}
	}
	if got := Export([]Block{blk(Paragraph, `\foo`)}); got != `\foo` {
		t.Errorf("a backslash that protects nothing is not doubled: %q", got)
	}
}

func TestCodeFenceOutgrowsBacktickLinesInTheCode(t *testing.T) {
	code := "before\n```\n````\nafter"
	bs := []Block{{Attrs: Attrs{Type: Code, Language: "md"}, Text: code}}
	md := Export(bs)
	if !strings.HasPrefix(md, "`````md\n") {
		t.Fatalf("fence too short: %q", md)
	}
	if got := Import(md); !slices.EqualFunc(got, bs, blockContentEqual) {
		t.Fatalf("got %+v", got)
	}
}

func TestImportToleratesUnclosedFenceAndHostileInput(t *testing.T) {
	got := Import("```py\nx = 1\n\ny = 2")
	if len(got) != 1 || got[0].Type != Code || got[0].Text != "x = 1\n\ny = 2" {
		t.Fatalf("unclosed fence: %+v", got)
	}
	got = Import("a\u001eb\xff\r\n")
	if len(got) != 1 || got[0].Text != "a�b�" {
		t.Fatalf("marker and invalid bytes must become U+FFFD: %+v", got)
	}
	if err := CheckLossless(Import(strings.Repeat(`\`, 1_000_000) + "#")); err != nil {
		t.Fatal(err)
	}
}

// randomLosslessBlocks draws blocks from the whole lossless subset, with
// text full of characters that mean something in Markdown.
func randomLosslessBlocks(r *rand.Rand) []Block {
	inline := []rune("ab #->|`*_[]\\&;0.1) 漢🙂")
	line := func() string { return randomRunes(r, inline, 8) }
	lines := func(prefix string) string {
		var out []string
		for range 1 + r.IntN(3) {
			out = append(out, prefix+line())
		}
		return strings.Join(out, "\n")
	}
	var bs []Block
	for range r.IntN(8) {
		b := Block{Attrs: Attrs{Type: Type(1 + r.IntN(11))}}
		switch b.Type {
		case Bulleted, Numbered, Todo:
			b.Indent = uint32(r.IntN(3))
			b.Checked = b.Type == Todo && r.IntN(2) == 0
			b.Text = line()
		case Quote:
			b.Text = lines("")
		case Code:
			b.Language = pick(r, []string{"", "go", "rust", "c++"})
			b.Text = pick(r, []string{"", lines(""), lines("`"), "```\n" + line()})
		case Table:
			b.Text = lines("|")
		case Divider:
		default:
			b.Text = line()
		}
		if CheckLossless([]Block{b}) == nil {
			bs = append(bs, b)
		}
	}
	return bs
}

func TestImportOfExportGivesBackLosslessBlocks(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	for i := 0; i < 20000; i++ {
		bs := randomLosslessBlocks(r)
		md := Export(bs)
		if got := Import(md); !slices.EqualFunc(got, bs, blockContentEqual) {
			t.Fatalf("round trip failed\nblocks %+v\nmarkdown %q\nimport  %+v", bs, md, got)
		}
	}
}

func TestExportOfImportIsAFixedPoint(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	alphabet := []rune("ab #->|`*_[]\\&;nbsp\n\n  1.)x漢")
	for i := 0; i < 20000; i++ {
		md := randomRunes(r, alphabet, 30)
		first := Import(md)
		if err := CheckLossless(first); err != nil {
			t.Fatalf("Import(%q) produced blocks outside the lossless subset: %v", md, err)
		}
		canonical := Export(first)
		if again := Export(Import(canonical)); again != canonical {
			t.Fatalf("not a fixed point\ninput %q\nonce  %q\ntwice %q", md, canonical, again)
		}
	}
}

func TestExportMappedPointsEveryTextCodePointAtItsSource(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	for i := 0; i < 5000; i++ {
		bs := randomLosslessBlocks(r)
		md, sources := ExportMapped(bs)
		if md != Export(bs) {
			t.Fatal("ExportMapped and Export disagree")
		}
		out := []rune(md)
		if len(out) != len(sources) {
			t.Fatalf("%d code points, %d sources", len(out), len(sources))
		}
		covered := make([]int, len(bs))
		for k, src := range sources {
			if src.Offset < 0 {
				continue
			}
			if text := []rune(bs[src.Block].Text); out[k] != text[src.Offset] {
				t.Fatalf("markdown %q position %d maps to block %d offset %d", md, k, src.Block, src.Offset)
			}
			covered[src.Block]++
		}
		for j, b := range bs {
			if covered[j] != len([]rune(b.Text)) {
				t.Fatalf("block %d: %d of %d code points mapped", j, covered[j], len([]rune(b.Text)))
			}
		}
	}
}

type seedBlock struct {
	NoteID   string  `json:"note_id"`
	Type     string  `json:"type"`
	Indent   uint32  `json:"indent"`
	Checked  *bool   `json:"checked"`
	Language *string `json:"language"`
	Text     string  `json:"text"`
}

// Every note of the small seed, written by people and the generator, round
// trips through Markdown, and its links match the seed's link pattern.
func TestSeedNotesRoundTripThroughMarkdown(t *testing.T) {
	f, err := os.Open("../../testdata/seed/small/blocks.jsonl")
	if err != nil {
		t.Skipf("seed data not available: %v", err)
	}
	defer f.Close()
	notes := map[string][]Block{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var sb seedBlock
		if err := json.Unmarshal(sc.Bytes(), &sb); err != nil {
			t.Fatal(err)
		}
		typ, ok := ParseType(sb.Type)
		if !ok {
			t.Fatalf("unknown type %q", sb.Type)
		}
		b := Block{Attrs: Attrs{Type: typ, Indent: sb.Indent}, Text: sb.Text}
		if sb.Checked != nil {
			b.Checked = *sb.Checked
		}
		if sb.Language != nil {
			b.Language = *sb.Language
		}
		if _, seen := notes[sb.NoteID]; !seen {
			order = append(order, sb.NoteID)
		}
		notes[sb.NoteID] = append(notes[sb.NoteID], b)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for _, id := range order {
		bs := notes[id]
		if err := CheckLossless(bs); err != nil {
			t.Fatalf("note %s: %v", id, err)
		}
		if got := Import(Export(bs)); !slices.EqualFunc(got, bs, blockContentEqual) {
			t.Fatalf("note %s does not round trip", id)
		}
	}
	if len(order) != 300 {
		t.Fatalf("%d notes, want 300", len(order))
	}
}

func FuzzImport(f *testing.F) {
	for _, s := range []string{"# a\n\n- b\n  1. c\n\n```go\nx\n```", "> a\n>\n| b", "\\\\# x\n&nbsp;", "- [x]\n---\n***"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, md string) {
		bs := Import(md)
		if err := CheckLossless(bs); err != nil {
			t.Fatalf("%q: %v", md, err)
		}
		canonical := Export(bs)
		if again := Export(Import(canonical)); again != canonical {
			t.Fatalf("not a fixed point: %q -> %q -> %q", md, canonical, again)
		}
	})
}
