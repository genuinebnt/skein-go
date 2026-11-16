package trigram

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/genuinebnt/skein-go/libs/dp"
)

const conformanceDir = "../../testdata/conformance/trigram"

// fixture is one shared behaviour the Go code and the Rust port must agree
// on. Input and expect are decoded per kind.
type fixture struct {
	Name   string          `json:"name"`
	Why    string          `json:"why"`
	Kind   string          `json:"kind"`
	Input  json.RawMessage `json:"input"`
	Expect json.RawMessage `json:"expect"`
}

type fixtureDoc struct {
	ID       DocID  `json:"id"`
	Text     string `json:"text"`
	Code     bool   `json:"code"`
	Language string `json:"language"`
}

type fixtureOptions struct {
	CaseSensitive bool     `json:"case_sensitive"`
	WholeWord     bool     `json:"whole_word"`
	CodeOnly      bool     `json:"code_only"`
	Languages     []string `json:"languages"`
}

type fixtureHit struct {
	Doc     DocID    `json:"doc"`
	Matches [][2]int `json:"matches"`
}

func decodeStrict(t *testing.T, raw []byte, into any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		t.Fatal(err)
	}
}

func TestConformanceFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(conformanceDir, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures in %s: %v", conformanceDir, err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fx fixture
			decodeStrict(t, raw, &fx)
			if fx.Why == "" {
				t.Fatal("fixture has no why")
			}
			got := runFixture(t, fx)
			var want any
			if err := json.Unmarshal(fx.Expect, &want); err != nil {
				t.Fatal(err)
			}
			gotJSON, _ := json.Marshal(got)
			var gotAny any
			_ = json.Unmarshal(gotJSON, &gotAny)
			if !reflect.DeepEqual(gotAny, want) {
				t.Fatalf("%s\n got  %s\n want %s", fx.Name, gotJSON, fx.Expect)
			}
		})
	}
}

func runFixture(t *testing.T, fx fixture) any {
	switch fx.Kind {
	case "fold":
		var in struct{ Text string }
		decodeStrict(t, fx.Input, &in)
		return map[string]any{"folded": FoldString(in.Text)}
	case "trigrams":
		var in struct{ Texts []string }
		decodeStrict(t, fx.Input, &in)
		out := make([][]string, len(in.Texts))
		for i, text := range in.Texts {
			out[i] = []string{}
			for _, tg := range Trigrams(text) {
				out[i] = append(out[i], tg.String())
			}
		}
		return map[string]any{"trigrams": out}
	case "query":
		var in struct {
			Pattern       string `json:"pattern"`
			CaseSensitive bool   `json:"case_sensitive"`
		}
		decodeStrict(t, fx.Input, &in)
		re, err := CompileRegexp(in.Pattern, in.CaseSensitive)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{"query": re.Query().String()}
	case "search":
		return runSearchFixture(t, fx)
	case "snippet":
		var in struct {
			Text    string `json:"text"`
			Matches []struct {
				Term  int `json:"term"`
				Start int `json:"start"`
				End   int `json:"end"`
			} `json:"matches"`
			Window int `json:"window"`
		}
		decodeStrict(t, fx.Input, &in)
		var matches []dp.Match
		for _, m := range in.Matches {
			matches = append(matches, dp.Match{Term: m.Term, Start: m.Start, End: m.End})
		}
		s, err := MakeSnippet(in.Text, matches, in.Window)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{"window": [2]int{s.Window.Start, s.Window.End}, "text": s.Text,
			"highlights": spansJSON(s.Highlights), "focus": [2]int{s.Focus.Start, s.Focus.End}}
	case "bitmap":
		var in struct{ Values []uint32 }
		decodeStrict(t, fx.Input, &in)
		data, err := BitmapOf(in.Values...).MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		var back Bitmap
		if err := back.UnmarshalBinary(data); err != nil {
			t.Fatal(err)
		}
		return map[string]any{"hex": hex.EncodeToString(data)}
	}
	t.Fatalf("unknown fixture kind %q", fx.Kind)
	return nil
}

func runSearchFixture(t *testing.T, fx fixture) any {
	var in struct {
		Docs     []fixtureDoc   `json:"docs"`
		Op       string         `json:"op"`
		Query    string         `json:"query"`
		MaxEdits int            `json:"max_edits"`
		Options  fixtureOptions `json:"options"`
	}
	decodeStrict(t, fx.Input, &in)
	docs := map[DocID]Doc{}
	for _, d := range in.Docs {
		docs[d.ID] = Doc{Text: d.Text, Code: d.Code, Language: d.Language}
	}
	ix := mustIndex(t, docs)
	opts := Options{CaseSensitive: in.Options.CaseSensitive, WholeWord: in.Options.WholeWord,
		CodeOnly: in.Options.CodeOnly, Languages: in.Options.Languages}
	var res Result
	switch in.Op {
	case "literal":
		res = ix.SearchLiteral(in.Query, opts)
	case "regex":
		var err error
		if res, err = ix.SearchRegexp(in.Query, opts); err != nil {
			t.Fatal(err)
		}
	case "fuzzy":
		got, err := ix.FuzzyCandidates(in.Query, in.MaxEdits, opts)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{"candidate_ids": got.ToArray()}
	default:
		t.Fatalf("unknown op %q", in.Op)
	}
	hits := []fixtureHit{}
	for _, h := range res.Hits {
		fh := fixtureHit{Doc: h.Doc}
		for _, m := range h.Matches {
			fh.Matches = append(fh.Matches, [2]int{m.Start, m.End})
		}
		hits = append(hits, fh)
	}
	return map[string]any{"candidates": res.Candidates, "hits": hits}
}

func spansJSON(spans []Span) [][2]int {
	out := [][2]int{}
	for _, s := range spans {
		out = append(out, [2]int{s.Start, s.End})
	}
	return out
}
