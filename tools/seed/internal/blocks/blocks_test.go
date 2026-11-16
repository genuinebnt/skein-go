package blocks

import (
	"errors"
	"strings"
	"testing"
)

func TestParseReadsEveryBlockKind(t *testing.T) {
	doc, err := Parse("---\ntitle: T\n---\n# H1\n\nPara one\ncontinues.\n\n- item\n  - nested\n- [x] done\n- [ ] open\n1. first\n> quote\n> more\n| a | b |\n| - | - |\n---\n```go\nfunc f() {}\n```\n")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Get("title") != "T" {
		t.Fatal("front matter not read")
	}
	types := []Type{Heading1, Paragraph, Bulleted, Bulleted, Todo, Todo, Numbered, Quote, Table, Divider, Code}
	if len(doc.Blocks) != len(types) {
		t.Fatalf("%d blocks: %+v", len(doc.Blocks), doc.Blocks)
	}
	for i, ty := range types {
		if doc.Blocks[i].Type != ty {
			t.Fatalf("block %d is %s, want %s", i, doc.Blocks[i].Type, ty)
		}
	}
	if doc.Blocks[1].Text != "Para one continues." || doc.Blocks[3].Indent != 1 || !doc.Blocks[4].Checked || doc.Blocks[7].Text != "quote\nmore" || doc.Blocks[10].Language != "go" {
		t.Fatalf("attributes wrong: %+v", doc.Blocks)
	}
}

func TestParseRejectsUnclosedCodeAndStrayIndent(t *testing.T) {
	if _, err := Parse("```go\nx\n"); err == nil {
		t.Fatal("unclosed code block accepted")
	}
	if _, err := Parse("  indented prose\n"); err == nil {
		t.Fatal("indented prose accepted")
	}
}

func TestComposeAndSplitRoundTrip(t *testing.T) {
	bs := []Block{{Type: Paragraph, Text: "a"}, {Type: Divider}, {Type: Code, Text: "x\ny"}}
	text, err := Compose(bs)
	if err != nil {
		t.Fatal(err)
	}
	prefix, parts := SplitText(text)
	if prefix != "" || len(parts) != 3 || parts[2] != "x\ny" || parts[1] != "" {
		t.Fatalf("split %q into %q %q", text, prefix, parts)
	}
	if _, err := Compose([]Block{{Text: "bad" + string(Marker)}}); !errors.Is(err, ErrMarkerInText) {
		t.Fatalf("marker in text: %v", err)
	}
	md := Markdown(bs)
	if !strings.Contains(md, "```\nx\ny\n```") || !strings.Contains(md, "---") {
		t.Fatalf("markdown %q", md)
	}
}
