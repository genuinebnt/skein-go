package blocks

import (
	"strings"
	"testing"
)

func BenchmarkElements10k(b *testing.B) {
	d := newDoc(b, 7)
	for i := 0; i < 100; i++ {
		if _, err := d.Sequence().Insert(d.Sequence().Len()/2, "\u001e"+strings.Repeat("word ", 20)); err != nil {
			b.Fatal(err)
		}
	}
	ops := d.Sequence().OpsSince(nil)
	b.ResetTimer()
	for b.Loop() {
		if _, err := Elements(ops); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseInlineParagraph(b *testing.B) {
	text := strings.Repeat("Some **bold** and *italic* text with `code`, a [[Wiki link]] and @mira. ", 20)
	for b.Loop() {
		ParseInline(text)
	}
}

func BenchmarkExportImport100Blocks(b *testing.B) {
	var bs []Block
	for i := 0; i < 100; i++ {
		bs = append(bs, Block{Attrs: Attrs{Type: Type(1 + i%9)}, Text: "Block text with **some** inline markup"})
	}
	for b.Loop() {
		Import(Export(bs))
	}
}
