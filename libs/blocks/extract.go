package blocks

import (
	"slices"
)

// Links returns the distinct [[link]] targets in the blocks' inline text,
// sorted ascending by bytes. Code blocks hold raw source and are skipped, as
// are code spans; that is the note's outbound link set for the link graph.
func Links(bs []Block) []string {
	return collect(bs, NodeWikiLink)
}

// Mentions returns the distinct @handles in the blocks' inline text, sorted
// ascending by bytes. Code blocks and code spans are skipped.
func Mentions(bs []Block) []string {
	return collect(bs, NodeMention)
}

func collect(bs []Block, kind NodeKind) []string {
	seen := map[string]struct{}{}
	var walk func([]Node)
	walk = func(ns []Node) {
		for _, n := range ns {
			if n.Kind == kind {
				seen[n.Value] = struct{}{}
			}
			walk(n.Children)
		}
	}
	for _, b := range bs {
		if b.Type != Code {
			walk(ParseInline(b.Text))
		}
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	slices.Sort(out)
	return out
}
