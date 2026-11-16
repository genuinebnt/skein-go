// Package blocks defines how the seed data lays a note's blocks out in its
// single CRDT text sequence, and parses the Markdown the hand-written anchor
// notes are authored in.
//
// # Layout
//
// ADR-0005 puts one sequence per note with a marker element at the start of
// each block. Version 1 of the wire format has no marker operation yet (spec
// section 10), so the seed data encodes a marker as an ordinary inserted
// character, Marker (U+001E, the ASCII record separator, which no editor
// lets a person type). A note's text is therefore
//
//	Marker block0-text Marker block1-text ...
//
// and a block's ID is the "replica:counter" of its marker's insert. Splitting
// a block (Enter) inserts a Marker; merging two (Backspace at the start)
// deletes one, exactly as the eventual marker operations will. Block
// attributes (type, indent, checked, language) are carried beside the text as
// seed metadata keyed by block ID, until markers carry last-writer-wins
// attributes in wire format version 2.
//
// # Invariants
//
//   - Block text never contains Marker, so splitting the note text at
//     markers recovers the blocks exactly.
//   - A note's text is empty or starts with Marker.
//
// # Porting notes
//
// Text is handled as Unicode code points ([]rune), matching the engine's
// positions (ADR-0003).
package blocks

import (
	"errors"
	"fmt"
	"strings"
)

// Marker is the code point that starts every block in a note's sequence.
const Marker = '\u001e'

// Type is a block's kind. The string values appear in blocks.jsonl.
type Type string

// The block types the seed data uses.
const (
	Paragraph Type = "paragraph"
	Heading1  Type = "heading1"
	Heading2  Type = "heading2"
	Heading3  Type = "heading3"
	Bulleted  Type = "bulleted"
	Numbered  Type = "numbered"
	Todo      Type = "todo"
	Quote     Type = "quote"
	Code      Type = "code"
	Divider   Type = "divider"
	Table     Type = "table"
)

// Block is one block: its attributes and its inline Markdown text (raw source
// for code blocks).
type Block struct {
	Type     Type
	Indent   int
	Checked  bool
	Language string
	Text     string
}

// ErrMarkerInText means a block's text contained Marker, which would make the
// note's layout ambiguous.
var ErrMarkerInText = errors.New("blocks: block text contains the block marker")

// Compose lays blocks out as one note text.
func Compose(bs []Block) (string, error) {
	var b strings.Builder
	for i, blk := range bs {
		if strings.ContainsRune(blk.Text, Marker) {
			return "", fmt.Errorf("%w: block %d", ErrMarkerInText, i)
		}
		b.WriteRune(Marker)
		b.WriteString(blk.Text)
	}
	return b.String(), nil
}

// SplitText splits a note text into block texts. Text before the first marker
// (which a valid note never has) is returned as prefix.
func SplitText(text string) (prefix string, texts []string) {
	parts := strings.Split(text, string(Marker))
	return parts[0], parts[1:]
}

// Markdown renders blocks back to Markdown, for chat transcripts and human
// inspection. It is the inverse of Parse for the subset Parse reads.
func Markdown(bs []Block) string {
	var b strings.Builder
	for i, blk := range bs {
		if i > 0 {
			b.WriteString("\n")
		}
		pad := strings.Repeat("  ", blk.Indent)
		switch blk.Type {
		case Heading1:
			b.WriteString("# " + blk.Text + "\n")
		case Heading2:
			b.WriteString("## " + blk.Text + "\n")
		case Heading3:
			b.WriteString("### " + blk.Text + "\n")
		case Bulleted:
			b.WriteString(pad + "- " + blk.Text + "\n")
		case Numbered:
			b.WriteString(pad + "1. " + blk.Text + "\n")
		case Todo:
			box := "[ ]"
			if blk.Checked {
				box = "[x]"
			}
			b.WriteString(pad + "- " + box + " " + blk.Text + "\n")
		case Quote:
			for _, line := range strings.Split(blk.Text, "\n") {
				b.WriteString("> " + line + "\n")
			}
		case Code:
			b.WriteString("```" + blk.Language + "\n" + blk.Text + "\n```\n")
		case Divider:
			b.WriteString("---\n")
		default: // Paragraph, Table
			b.WriteString(blk.Text + "\n")
		}
	}
	return b.String()
}
