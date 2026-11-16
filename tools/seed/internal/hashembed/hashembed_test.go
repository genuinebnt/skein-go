package hashembed

import (
	"math"
	"testing"
)

func TestTokensKeepMarksAndSplitCJK(t *testing.T) {
	got := Tokens("The leader election ゲーム क्षि of a b")
	want := []string{"leader", "election", "ゲ", "ー", "ム", "क्षि"}
	if len(got) != len(want) {
		t.Fatalf("tokens %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("tokens %q, want %q", got, want)
		}
	}
}

func TestEmbeddingIsDeterministicNormalisedAndTopical(t *testing.T) {
	a := Embed("raft leader election heartbeat quorum term")
	b := Embed("raft leader election heartbeat quorum term")
	if a != b {
		t.Fatal("same text gave different vectors")
	}
	norm := 0.0
	for _, x := range a {
		norm += x * x
	}
	if math.Abs(norm-1) > 1e-3 {
		t.Fatalf("norm %f", norm)
	}
	same := Cosine(a, Embed("the raft leader sends a heartbeat each term"))
	other := Cosine(a, Embed("react component hydration suspense render"))
	if same <= other {
		t.Fatalf("shared words should be closer: %f vs %f", same, other)
	}
	if Embed("") != ([Dim]float64{}) {
		t.Fatal("empty text should embed to zero")
	}
}
