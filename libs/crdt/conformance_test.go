package crdt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// conformanceDir holds the language-neutral fixtures shared with the Rust
// port. The format is specified in docs/spec/crdt-sequence.md.
const conformanceDir = "../../testdata/conformance/crdt"

type fixture struct {
	Name     string               `json:"name"`
	Why      string               `json:"why"`
	Replicas map[string]ReplicaID `json:"replicas"`
	Steps    []fixtureStep        `json:"steps"`
	Expect   map[string]string    `json:"expect"`
}

// fixtureStep is exactly one of: a local insert, a local delete, a sync, or a
// mid-test expectation.
type fixtureStep struct {
	Replica string `json:"replica"`
	Insert  *struct {
		Index int    `json:"index"`
		Text  string `json:"text"`
	} `json:"insert"`
	Delete *struct {
		Index int `json:"index"`
		Count int `json:"count"`
	} `json:"delete"`
	Sync   json.RawMessage   `json:"sync"`
	Expect map[string]string `json:"expect"`
}

func TestConformanceFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(conformanceDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatalf("no fixtures found in %s", conformanceDir)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) { runFixture(t, path) })
	}
}

func runFixture(t *testing.T, path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("parse: %v", err)
	}

	names := make([]string, 0, len(fx.Replicas))
	seqs := map[string]*Sequence{}
	for name, id := range fx.Replicas {
		names = append(names, name)
		seqs[name] = newSeq(t, id)
	}
	sort.Strings(names) // map order is random; sync order must not be

	get := func(name string) *Sequence {
		s, ok := seqs[name]
		if !ok {
			t.Fatalf("unknown replica %q", name)
		}
		return s
	}
	check := func(expect map[string]string, when string) {
		for who, want := range expect {
			targets := []string{who}
			if who == "all" {
				targets = names
			}
			for _, name := range targets {
				if got := get(name).Text(); got != want {
					t.Fatalf("%s: replica %s has %q, want %q (%s)", when, name, got, want, fx.Why)
				}
			}
		}
	}

	for i, step := range fx.Steps {
		switch {
		case step.Insert != nil:
			mustInsert(t, get(step.Replica), step.Insert.Index, step.Insert.Text)
		case step.Delete != nil:
			mustDelete(t, get(step.Replica), step.Delete.Index, step.Delete.Count)
		case step.Sync != nil:
			var all string
			if json.Unmarshal(step.Sync, &all) == nil && all == "all" {
				ordered := make([]*Sequence, 0, len(names))
				for _, n := range names {
					ordered = append(ordered, seqs[n])
				}
				syncAll(t, ordered...)
				continue
			}
			var pair struct{ From, To string }
			if err := json.Unmarshal(step.Sync, &pair); err != nil {
				t.Fatalf("step %d: bad sync: %v", i, err)
			}
			from, to := get(pair.From), get(pair.To)
			mustApply(t, to, from.OpsSince(to.Version()))
		case step.Expect != nil:
			check(step.Expect, "after step "+filepath.Base(path))
		default:
			t.Fatalf("step %d does nothing", i)
		}
	}
	check(fx.Expect, "at the end")
}
