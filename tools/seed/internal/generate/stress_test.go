package generate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestStressShapesArePathological realizes the six shape notes of the
// stress profile (the 150,000 graph notes are tiny and covered by the
// generator's own truth checks) and checks each really is the edge case it
// claims to be. Every note is also replay-verified inside realize.
func TestStressShapesArePathological(t *testing.T) {
	if testing.Short() {
		t.Skip("stress shapes take a few seconds")
	}
	prof, err := LookupProfile("stress")
	if err != nil {
		t.Fatal(err)
	}
	p, err := newPlan(prof, 1234)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range p.workspaces {
		for i := range w.Regexes {
			w.Regexes[i].compiled()
		}
	}
	found := 0
	for _, n := range p.notes {
		if n.Stress == "" || n.Stress == "chain" || n.Stress == "hub" || n.Stress == "core" || n.Stress == "spoke" {
			continue
		}
		found++
		res, err := p.realize(n.Index)
		if err != nil {
			t.Fatalf("%s: %v", n.Title, err)
		}
		text := res.final.Text
		switch n.Stress {
		case "huge-text":
			if l := utf8.RuneCountInString(text); l < 200000 {
				t.Errorf("huge note has %d characters", l)
			}
		case "huge-code":
			for _, b := range res.finalBlk {
				if lines := strings.Count(b.Block.Text, "\n") + 1; lines < 2500 {
					t.Errorf("code block has %d lines", lines)
				}
			}
		case "deep-indent":
			deepest := 0
			for _, b := range res.finalBlk {
				deepest = max(deepest, b.Block.Indent)
			}
			if deepest != 64 {
				t.Errorf("deepest indent %d", deepest)
			}
		case "unicode-planes":
			planes := map[rune]bool{}
			for _, r := range text {
				planes[r>>16] = true
			}
			if len(planes) != 17 {
				t.Errorf("text covers %d Unicode planes, want 17", len(planes))
			}
		case "many-replicas":
			if res.final.Replicas < stressReplicas {
				t.Errorf("%d replicas edited the note", res.final.Replicas)
			}
		case "many-batches":
			if res.final.Batches != 401 {
				t.Errorf("%d batches", res.final.Batches)
			}
		}
	}
	if found != len(stressShapes) {
		t.Fatalf("found %d shape notes, want %d", found, len(stressShapes))
	}
}
