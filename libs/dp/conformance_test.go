package dp

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// conformanceDir holds the language-neutral fixtures shared with the Rust
// port. The format is described in its README.md and docs/spec/dp.md.
const conformanceDir = "../../testdata/conformance/dp"

type fixture struct {
	Name   string          `json:"name"`
	Why    string          `json:"why"`
	Kind   string          `json:"kind"`
	Input  json.RawMessage `json:"input"`
	Expect json.RawMessage `json:"expect"`
}

type fixtureHunk struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type fixtureNote struct {
	ID            string   `json:"id"`
	Minutes       int      `json:"minutes"`
	Value         int64    `json:"value"`
	Prerequisites []string `json:"prerequisites"`
}

type fixtureSnippet struct {
	Found bool `json:"found"`
	Span  struct {
		Start int `json:"start"`
		End   int `json:"end"`
	} `json:"span"`
	Terms int `json:"terms"`
}

type fixtureSegmentation struct {
	Words []string `json:"words"`
	Cost  int64    `json:"cost"`
}

func TestConformanceFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(conformanceDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 10 {
		t.Fatalf("found %d fixtures in %s, want at least 10", len(paths), conformanceDir)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) { runFixture(t, path) })
	}
}

// decodeStrict rejects unknown fields so a misspelt key in a fixture fails
// instead of being silently ignored.
func decodeStrict(t *testing.T, raw []byte, into any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func runFixture(t *testing.T, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	decodeStrict(t, raw, &fx)
	if fx.Why == "" || fx.Name == "" {
		t.Fatal("fixture needs a name and a why")
	}
	got := runFixtureKind(t, fx)
	// Compare as decoded JSON so the expectation's field order and number
	// formatting do not matter.
	var want, have any
	if err := json.Unmarshal(fx.Expect, &want); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &have); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(have, want) {
		t.Fatalf("%s\n got  %s\n want %s\nwhy: %s", fx.Name, encoded, fx.Expect, fx.Why)
	}
}

// runFixtureKind runs the algorithm a fixture names and returns its result
// in the fixture's expect shape.
func runFixtureKind(t *testing.T, fx fixture) any {
	switch fx.Kind {
	case "levenshtein", "damerau_levenshtein":
		var in struct{ A, B string }
		decodeStrict(t, fx.Input, &in)
		d := Levenshtein(in.A, in.B)
		if fx.Kind == "damerau_levenshtein" {
			d = DamerauLevenshtein(in.A, in.B)
		}
		return map[string]any{"distance": d}
	case "levenshtein_within":
		var in struct {
			A           string `json:"a"`
			B           string `json:"b"`
			MaxDistance int    `json:"max_distance"`
		}
		decodeStrict(t, fx.Input, &in)
		d, ok := LevenshteinWithin(in.A, in.B, in.MaxDistance)
		return map[string]any{"distance": d, "within": ok}
	case "lcs":
		var in struct{ A, B string }
		decodeStrict(t, fx.Input, &in)
		lcs := LCS([]rune(in.A), []rune(in.B))
		return map[string]any{"length": LCSLength([]rune(in.A), []rune(in.B)), "lcs": string(lcs)}
	case "diff":
		return runDiffFixture(t, fx)
	case "reading_plan":
		return runPlanFixture(t, fx)
	case "chunk":
		var in struct {
			Tokens []int `json:"tokens"`
			Limit  int   `json:"limit"`
		}
		decodeStrict(t, fx.Input, &in)
		c, err := ChunkBlocks(in.Tokens, in.Limit)
		switch {
		case errors.Is(err, ErrBlockTooLarge):
			return map[string]any{"error": "block_too_large"}
		case err != nil:
			t.Fatal(err)
		}
		return map[string]any{"ends": c.Ends, "cost": c.Cost}
	case "snippet":
		var in struct {
			Window  int `json:"window"`
			Matches []struct {
				Term  int `json:"term"`
				Start int `json:"start"`
				End   int `json:"end"`
			} `json:"matches"`
		}
		decodeStrict(t, fx.Input, &in)
		matches := make([]Match, len(in.Matches))
		for i, m := range in.Matches {
			matches[i] = Match{Term: m.Term, Start: m.Start, End: m.End}
		}
		s, ok, err := BestSnippet(matches, in.Window)
		if err != nil {
			t.Fatal(err)
		}
		var out fixtureSnippet
		out.Found, out.Terms = ok, s.Terms
		out.Span.Start, out.Span.End = s.Span.Start, s.Span.End
		return out
	case "segment":
		var in struct {
			Frequencies map[string]uint64 `json:"frequencies"`
			Text        string            `json:"text"`
		}
		decodeStrict(t, fx.Input, &in)
		s, err := NewSegmenter(in.Frequencies)
		if err != nil {
			t.Fatal(err)
		}
		seg := s.Segment(in.Text)
		return fixtureSegmentation{Words: seg.Words, Cost: seg.Cost}
	}
	t.Fatalf("unknown fixture kind %q", fx.Kind)
	return nil
}

func runDiffFixture(t *testing.T, fx fixture) any {
	var in struct {
		A         string `json:"a"`
		B         string `json:"b"`
		Tokens    string `json:"tokens"`
		Algorithm string `json:"algorithm"`
	}
	decodeStrict(t, fx.Input, &in)
	tokenisers := map[string]func(string) []string{"lines": Lines, "words": Words, "chars": Chars}
	tokenise, ok := tokenisers[in.Tokens]
	if !ok {
		t.Fatalf("unknown tokens %q", in.Tokens)
	}
	var hunks []Hunk[string]
	switch in.Algorithm {
	case "myers":
		hunks = Diff(tokenise(in.A), tokenise(in.B))
	case "patience":
		hunks = PatienceDiff(tokenise(in.A), tokenise(in.B))
	default:
		t.Fatalf("unknown algorithm %q", in.Algorithm)
	}
	out := []fixtureHunk{}
	for _, h := range joinHunks(hunks) {
		out = append(out, fixtureHunk{Kind: h.Kind.String(), Text: h.Text})
	}
	return map[string]any{"hunks": out}
}

func runPlanFixture(t *testing.T, fx fixture) any {
	var in struct {
		Budget    int           `json:"budget"`
		NodeLimit int           `json:"node_limit"`
		Notes     []fixtureNote `json:"notes"`
	}
	decodeStrict(t, fx.Input, &in)
	notes := make([]Note, len(in.Notes))
	for i, n := range in.Notes {
		notes[i] = Note{ID: n.ID, Minutes: n.Minutes, Value: n.Value, Prerequisites: n.Prerequisites}
	}
	plan, err := PlanReading(notes, in.Budget, PlanOptions{NodeLimit: in.NodeLimit})
	var cycle *CycleError
	switch {
	case errors.As(err, &cycle):
		return map[string]any{"error": "cyclic_prerequisites", "cycle": cycle.Cycle}
	case err != nil:
		t.Fatal(err)
	}
	if err := CheckPlan(notes, in.Budget, plan); err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"notes":         planIDs(plan),
		"total_minutes": plan.TotalMinutes,
		"total_value":   plan.TotalValue,
		"method":        plan.Method.String(),
		"optimal":       plan.Optimal,
	}
}
