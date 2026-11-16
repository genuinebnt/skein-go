package graph

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// conformanceDir holds the language-neutral fixtures shared with the Rust
// port. The format is specified in docs/spec/graph.md.
const conformanceDir = "../../testdata/conformance/graph"

// floatTolerance is the absolute tolerance for every number in a fixture.
const floatTolerance = 1e-9

type fixture struct {
	Name  string `json:"name"`
	Why   string `json:"why"`
	Graph struct {
		Nodes []struct {
			ID   string `json:"id"`
			Root bool   `json:"root"`
		} `json:"nodes"`
		Edges []struct {
			From   string  `json:"from"`
			To     string  `json:"to"`
			Weight float64 `json:"weight"`
		} `json:"edges"`
	} `json:"graph"`
	BuildError string         `json:"build_error"`
	Queries    []fixtureQuery `json:"queries"`
}

type fixtureQuery struct {
	Query struct {
		Op            string         `json:"op"`
		Source        string         `json:"source"`
		From          string         `json:"from"`
		To            string         `json:"to"`
		Note          string         `json:"note"`
		Direction     string         `json:"direction"`
		MaxHops       int            `json:"max_hops"`
		Partition     map[string]int `json:"partition"`
		Damping       float64        `json:"damping"`
		Tolerance     float64        `json:"tolerance"`
		MaxIterations int            `json:"max_iterations"`
	} `json:"query"`
	Expected map[string]any `json:"expected"`
	Why      string         `json:"why"`
}

// errorNames maps sentinels to the snake-case names fixtures use.
var errorNames = map[error]string{
	ErrEmptyID:         "empty_id",
	ErrDuplicateNode:   "duplicate_node",
	ErrUnknownNode:     "unknown_node",
	ErrSelfLoop:        "self_loop",
	ErrDuplicateEdge:   "duplicate_edge",
	ErrInvalidWeight:   "invalid_weight",
	ErrInvalidArgument: "invalid_argument",
}

func errorName(err error) string {
	for sentinel, name := range errorNames {
		if errors.Is(err, sentinel) {
			return name
		}
	}
	return "unexpected: " + err.Error()
}

var directionNames = map[string]Direction{"cited_by": CitedBy, "cites": Cites, "undirected": Undirected}

func TestConformanceFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(conformanceDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 8 {
		t.Fatalf("found %d fixtures in %s, want at least 8", len(paths), conformanceDir)
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
	if fx.Why == "" {
		t.Fatal("fixture has no why")
	}
	var nodes []Node
	for _, n := range fx.Graph.Nodes {
		nodes = append(nodes, Node{ID: n.ID, Root: n.Root})
	}
	var edges []Edge
	for _, e := range fx.Graph.Edges {
		edges = append(edges, Edge{From: e.From, To: e.To, Weight: e.Weight})
	}
	g, err := New(nodes, edges)
	if fx.BuildError != "" {
		if err == nil || errorName(err) != fx.BuildError {
			t.Fatalf("New: %v, want %s (%s)", err, fx.BuildError, fx.Why)
		}
		return
	}
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := g.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	for i, q := range fx.Queries {
		if q.Why == "" {
			t.Errorf("query %d (%s) has no why", i, q.Query.Op)
		}
		got := toJSON(t, runQuery(g, q))
		for key, want := range q.Expected {
			if !jsonClose(got[key], want) {
				t.Errorf("query %d (%s): %s = %s, want %s\nwhy: %s", i, q.Query.Op, key, mustMarshal(got[key]), mustMarshal(want), q.Why)
			}
		}
	}
}

// runQuery returns the query's result in fixture shape, or {"error": name}.
func runQuery(g *Graph, q fixtureQuery) map[string]any {
	in := q.Query
	result, err := func() (map[string]any, error) {
		switch in.Op {
		case "distances":
			hops, err := g.Distances(in.Source)
			return map[string]any{"hops": hops}, err
		case "shortest_path", "weighted_path":
			find := g.ShortestPath
			if in.Op == "weighted_path" {
				find = g.ShortestWeightedPath
			}
			p, err := find(in.From, in.To)
			return map[string]any{"found": p.Found, "path": p.Nodes, "cost": p.Cost}, err
		case "blast_radius":
			dir, ok := directionNames[in.Direction]
			if !ok {
				return nil, fmt.Errorf("%w: direction %q", ErrInvalidArgument, in.Direction)
			}
			r, err := g.BlastRadius(in.Source, dir, in.MaxHops)
			return map[string]any{"hops": r.Hops, "ring_sizes": r.Sizes}, err
		case "components":
			p := g.ConnectedComponents()
			return map[string]any{"of": p.Of, "members": p.Members, "orphans": g.OrphanComponents()}, nil
		case "scc":
			p := g.StronglyConnected()
			return map[string]any{"of": p.Of, "members": p.Members}, nil
		case "cycle":
			return map[string]any{"cycle": g.FindCycle()}, nil
		case "topological_order":
			o := g.TopologicalOrder()
			return map[string]any{"order": o.Order, "unplaced": o.Unplaced, "layers": o.Layers}, nil
		case "betweenness":
			return map[string]any{"scores": g.Betweenness()}, nil
		case "pagerank":
			r, err := g.PageRank(PageRankOptions{Damping: in.Damping, Tolerance: in.Tolerance, MaxIterations: in.MaxIterations})
			return map[string]any{"scores": r.Scores, "converged": r.Converged}, err
		case "louvain":
			c := g.Louvain()
			return map[string]any{"of": c.Partition.Of, "members": c.Partition.Members, "modularity": c.Modularity}, nil
		case "modularity":
			q, err := g.Modularity(in.Partition)
			return map[string]any{"modularity": q}, err
		case "reading_path":
			r, err := g.ReadingPath(in.Note)
			steps := make([]map[string]any, len(r.Steps))
			for i, s := range r.Steps {
				steps[i] = map[string]any{"note": s.Note, "layer": s.Layer, "hops": s.Hops, "via": s.Via}
			}
			return map[string]any{"steps": steps, "cycles": r.Cycles}, err
		case "diameter":
			d := g.Diameter()
			return map[string]any{"length": d.Length, "from": d.From, "to": d.To}, nil
		default:
			return nil, fmt.Errorf("unknown op %q", in.Op)
		}
	}()
	if err != nil {
		return map[string]any{"error": errorName(err)}
	}
	return result
}

// toJSON round-trips a result through encoding/json so it compares with the
// decoded fixture value for value (numbers become float64, slices []any).
func toJSON(t *testing.T, v map[string]any) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(mustMarshal(v)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// jsonClose compares decoded JSON values, numbers within floatTolerance. A
// null and an empty list are different: results promise empty lists.
func jsonClose(got, want any) bool {
	switch w := want.(type) {
	case float64:
		g, ok := got.(float64)
		return ok && math.Abs(g-w) <= floatTolerance
	case []any:
		g, ok := got.([]any)
		return ok && slices.EqualFunc(g, w, jsonClose)
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for k, wv := range w {
			if gv, ok := g[k]; !ok || !jsonClose(gv, wv) {
				return false
			}
		}
		return true
	default:
		return got == want
	}
}
