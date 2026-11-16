package blocks

import (
	"fmt"

	"github.com/genuinebnt/skein-go/libs/crdt"
)

// Marker is the code point that starts every block in a note's sequence
// (U+001E, the ASCII record separator, which no editor lets a person type).
// Wire format v1 has no marker operation yet, so a marker is an ordinary
// inserted element with this value (docs/seed-data.md).
const Marker = '\u001e'

// Type is a block's kind. The numeric values are part of the attribute wire
// format (docs/spec/blocks.md section 4) and must not change; the names are
// the strings used in blocks.jsonl and in events.
type Type uint8

// The block types. Paragraph is the type of a block that has never had a
// type written.
const (
	Paragraph Type = 1
	Heading1  Type = 2
	Heading2  Type = 3
	Heading3  Type = 4
	Bulleted  Type = 5
	Numbered  Type = 6
	Todo      Type = 7
	Quote     Type = 8
	Code      Type = 9
	Divider   Type = 10
	Table     Type = 11
)

var typeNames = [...]string{
	Paragraph: "paragraph",
	Heading1:  "heading1",
	Heading2:  "heading2",
	Heading3:  "heading3",
	Bulleted:  "bulleted",
	Numbered:  "numbered",
	Todo:      "todo",
	Quote:     "quote",
	Code:      "code",
	Divider:   "divider",
	Table:     "table",
}

// Valid reports whether t is one of the defined types.
func (t Type) Valid() bool { return t >= Paragraph && t <= Table }

// String returns the type's name, as used in blocks.jsonl and events.
func (t Type) String() string {
	if t.Valid() {
		return typeNames[t]
	}
	return fmt.Sprintf("Type(%d)", uint8(t))
}

// IsList reports whether the type is a list item, the only kinds whose
// indent Markdown can represent.
func (t Type) IsList() bool { return t == Bulleted || t == Numbered || t == Todo }

// ParseType returns the type with the given name.
func ParseType(name string) (Type, bool) {
	for t := Paragraph; t <= Table; t++ {
		if typeNames[t] == name {
			return t, true
		}
	}
	return 0, false
}

// MaxIndent is the deepest indent level a block may have. The stress seed
// profile nests bullets 64 levels deep; the limit leaves room above that
// while keeping a hostile write from producing megabytes of Markdown
// indentation.
const MaxIndent = 128

// MaxLanguageBytes bounds a code block's language name.
const MaxLanguageBytes = 64

// Attrs are a block's attributes. The zero value is not valid; DefaultAttrs
// is what a block has before anything is written.
type Attrs struct {
	Type     Type
	Indent   uint32
	Checked  bool
	Language string
}

// DefaultAttrs are the attributes of a block with no attribute writes: a
// paragraph, not indented, unchecked, no language.
func DefaultAttrs() Attrs { return Attrs{Type: Paragraph} }

// Block is one block of a note: its attributes and its text, which is inline
// Markdown (raw source for code blocks) and never contains Marker.
//
// ID is the ID of the block's marker element, or crdt.Root for text that
// precedes the first marker (a leading block, which a note only has after
// its first marker was deleted or text was inserted before it). Chars holds
// the element ID of each code point of Text, in order; it is nil for blocks
// that did not come from a sequence (for example Markdown import).
type Block struct {
	ID crdt.ID
	Attrs
	Text  string
	Chars []crdt.ID
}

// Content returns the block without its identity: attributes and text only.
// Markdown round trips compare content, because Markdown carries no IDs.
func (b Block) Content() Block { return Block{Attrs: b.Attrs, Text: b.Text} }
