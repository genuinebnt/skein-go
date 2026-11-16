package generate

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/tools/seed/internal/corpus"
	"github.com/genuinebnt/skein-go/tools/seed/internal/corpus/anchor"
	"github.com/genuinebnt/skein-go/tools/seed/internal/graphs"
	"github.com/genuinebnt/skein-go/tools/seed/internal/hashembed"
	"github.com/genuinebnt/skein-go/tools/seed/internal/policy"
	"github.com/genuinebnt/skein-go/tools/seed/internal/prng"
)

var topicIDs = func() []string {
	out := make([]string, len(corpus.Topics))
	for i, t := range corpus.Topics {
		out[i] = t.ID
	}
	return out
}()

// maxListed caps the hop maps and paths written inline; larger results are
// summarised (ring sizes, distance) and marked truncated.
const maxListed = 2000

// Truth record types.

// GraphSummaryRecord is a line of truth/graph/summary.jsonl.
type GraphSummaryRecord struct {
	WorkspaceID      string `json:"workspace_id"`
	Nodes            int    `json:"nodes"`
	Edges            int    `json:"edges"`
	NontrivialSCCs   int    `json:"nontrivial_sccs"`
	TrivialSCCs      int    `json:"trivial_sccs"`
	LargestSCC       int    `json:"largest_scc"`
	Components       int    `json:"components"`
	OrphanComponents int    `json:"orphan_components"`
	DanglingLinks    int    `json:"dangling_links"`
	Acyclic          bool   `json:"acyclic"`
}

// SCCRecord is a line of truth/graph/sccs.jsonl.
type SCCRecord struct {
	WorkspaceID string   `json:"workspace_id"`
	Index       int      `json:"index"`
	Size        int      `json:"size"`
	Members     []string `json:"members"`
	Planted     *string  `json:"planted"`
}

// CycleRecord is a line of truth/graph/cycles.jsonl.
type CycleRecord struct {
	WorkspaceID string   `json:"workspace_id"`
	Kind        string   `json:"kind"`
	Label       string   `json:"label"`
	Cycle       []string `json:"cycle"`
}

// ComponentRecord is a line of truth/graph/components.jsonl.
type ComponentRecord struct {
	WorkspaceID string   `json:"workspace_id"`
	Index       int      `json:"index"`
	Size        int      `json:"size"`
	HasRoot     bool     `json:"has_root"`
	Orphan      bool     `json:"orphan"`
	Planted     bool     `json:"planted"`
	Members     []string `json:"members"`
}

// HubRecord is a line of truth/graph/hubs.jsonl.
type HubRecord struct {
	WorkspaceID string `json:"workspace_id"`
	NoteID      string `json:"note_id"`
	Title       string `json:"title"`
	By          string `json:"by"`
	Rank        int    `json:"rank"`
	InDegree    int    `json:"in_degree"`
	OutDegree   int    `json:"out_degree"`
	Planted     bool   `json:"planted"`
}

// BlastRecord is a line of truth/graph/blast_radius.jsonl.
type BlastRecord struct {
	WorkspaceID string         `json:"workspace_id"`
	NoteID      string         `json:"note_id"`
	Direction   string         `json:"direction"`
	MaxHops     int            `json:"max_hops"`
	Reached     int            `json:"reached"`
	RingSizes   []int          `json:"ring_sizes"`
	Hops        map[string]int `json:"hops"`
	Truncated   bool           `json:"truncated"`
	Why         string         `json:"why"`
}

// PathRecord is a line of truth/graph/shortest_paths.jsonl.
type PathRecord struct {
	WorkspaceID string   `json:"workspace_id"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	Metric      string   `json:"metric"`
	Found       bool     `json:"found"`
	Distance    int      `json:"distance"`
	Path        []string `json:"path"`
	PathCount   int      `json:"path_count"`
	Unique      bool     `json:"unique"`
	Truncated   bool     `json:"truncated"`
}

// CommunityRecord is a line of truth/graph/communities.jsonl.
type CommunityRecord struct {
	WorkspaceID string `json:"workspace_id"`
	NoteID      string `json:"note_id"`
	Community   string `json:"community"`
}

// BridgeRecord is a line of truth/graph/bridges.jsonl.
type BridgeRecord struct {
	WorkspaceID   string `json:"workspace_id"`
	From          string `json:"from"`
	To            string `json:"to"`
	FromCommunity string `json:"from_community"`
	ToCommunity   string `json:"to_community"`
}

// DanglingRecord is a line of truth/graph/dangling.jsonl.
type DanglingRecord struct {
	WorkspaceID  string `json:"workspace_id"`
	SourceNoteID string `json:"source_note_id"`
	TargetTitle  string `json:"target_title"`
	BlockID      string `json:"block_id"`
}

// Qrel is one relevance judgement.
type Qrel struct {
	NoteID string `json:"note_id"`
	Grade  int    `json:"grade"`
}

// TextQueryRecord is a line of truth/search/text_queries.jsonl.
type TextQueryRecord struct {
	ID            string   `json:"id"`
	WorkspaceID   string   `json:"workspace_id"`
	Query         string   `json:"query"`
	Terms         []string `json:"terms"`
	Kind          string   `json:"kind"`
	SubjectUserID string   `json:"subject_user_id"`
	Qrels         []Qrel   `json:"qrels"`
}

// TypoQueryRecord is a line of truth/search/typo_queries.jsonl.
type TypoQueryRecord struct {
	ID            string   `json:"id"`
	WorkspaceID   string   `json:"workspace_id"`
	Query         string   `json:"query"`
	Intended      string   `json:"intended"`
	EditDistance  int      `json:"edit_distance"`
	TargetQueryID string   `json:"target_query_id"`
	TargetNoteIDs []string `json:"target_note_ids"`
}

// RegexQueryRecord is a line of truth/search/regex_queries.jsonl.
type RegexQueryRecord struct {
	ID            string       `json:"id"`
	WorkspaceID   string       `json:"workspace_id"`
	Pattern       string       `json:"pattern"`
	CaseSensitive bool         `json:"case_sensitive"`
	CodeOnly      bool         `json:"code_only"`
	Languages     []string     `json:"languages"`
	MatchCount    int          `json:"match_count"`
	Matches       []regexMatch `json:"matches"`
}

// TitleMatch is one expected title suggestion.
type TitleMatch struct {
	NoteID string `json:"note_id"`
	Title  string `json:"title"`
}

// TitlePrefixRecord is a line of truth/search/title_prefixes.jsonl.
type TitlePrefixRecord struct {
	WorkspaceID    string       `json:"workspace_id"`
	Prefix         string       `json:"prefix"`
	Typo           bool         `json:"typo"`
	Expected       []TitleMatch `json:"expected"`
	IntendedNoteID *string      `json:"intended_note_id"`
	EditDistance   int          `json:"edit_distance"`
}

// SuggestedLinkRecord is a line of truth/discovery/suggested_links.jsonl.
type SuggestedLinkRecord struct {
	WorkspaceID string   `json:"workspace_id"`
	A           string   `json:"a"`
	B           string   `json:"b"`
	SharedTerms []string `json:"shared_terms"`
	Cosine      float64  `json:"hash_cosine"`
	RankBForA   int      `json:"hash_rank_b_for_a"`
	RankAForB   int      `json:"hash_rank_a_for_b"`
}

// NearDuplicateRecord is a line of truth/discovery/near_duplicates.jsonl.
type NearDuplicateRecord struct {
	WorkspaceID string  `json:"workspace_id"`
	Original    string  `json:"original_note_id"`
	Duplicate   string  `json:"duplicate_note_id"`
	Jaccard     float64 `json:"jaccard_3_shingles"`
	Cosine      float64 `json:"hash_cosine"`
	Edits       string  `json:"edits"`
}

// NoteTopicRecord is a line of truth/clustering/note_topics.jsonl.
type NoteTopicRecord struct {
	NoteID         string  `json:"note_id"`
	WorkspaceID    string  `json:"workspace_id"`
	Topic          *string `json:"topic"`
	SecondaryTopic *string `json:"secondary_topic"`
	Mix            float64 `json:"mix"`
	Kind           string  `json:"kind"`
}

// MatrixSubject is who a permission check is for.
type MatrixSubject struct {
	UserID      *string `json:"user_id"`
	AgentRunID  *string `json:"agent_run_id"`
	ShareLinkID *string `json:"share_link_id"`
}

// PermissionRecord is a line of truth/permissions/matrix.jsonl.
type PermissionRecord struct {
	WorkspaceID string        `json:"workspace_id"`
	Subject     MatrixSubject `json:"subject"`
	Action      string        `json:"action"`
	ResourceID  string        `json:"resource_id"`
	At          string        `json:"at"`
	Expected    string        `json:"expected"`
	Rule        string        `json:"rule"`
}

// EvalCitation is a resolved citation.
type EvalCitation struct {
	NoteID  string `json:"note_id"`
	BlockID string `json:"block_id"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Quote   string `json:"quote"`
}

// EvalConfirmation is the confirmation an eval expects.
type EvalConfirmation struct {
	Tool          string   `json:"tool"`
	TargetNoteIDs []string `json:"target_note_ids"`
}

// EvalRecord is a line of truth/agent/evals.jsonl.
type EvalRecord struct {
	ID             string            `json:"id"`
	WorkspaceID    string            `json:"workspace_id"`
	UserID         string            `json:"user_id"`
	CastKey        string            `json:"cast_key"`
	Prompt         string            `json:"prompt"`
	Outcome        string            `json:"outcome"`
	MustInclude    []string          `json:"must_include"`
	MustNotInclude []string          `json:"must_not_include"`
	Citations      []EvalCitation    `json:"citations"`
	ExpectedTools  []string          `json:"expected_tools"`
	ForbiddenTools []string          `json:"forbidden_tools"`
	Confirmation   *EvalConfirmation `json:"confirmation"`
	Why            string            `json:"why"`
}

// workspaceGraph is one workspace's final link graph over its non-trashed
// notes.
type workspaceGraph struct {
	w     *wsPlan
	nodes []int       // global note index per local node
	local map[int]int // global note index -> local node
	g     *graphs.Graph
}

func (a *aggregate) graphOf(w *wsPlan) *workspaceGraph {
	wg := &workspaceGraph{w: w, local: map[int]int{}}
	for _, ni := range w.Notes {
		if !a.p.notes[ni].Trashed {
			wg.local[ni] = len(wg.nodes)
			wg.nodes = append(wg.nodes, ni)
		}
	}
	wg.g = graphs.New(len(wg.nodes))
	for _, ni := range wg.nodes {
		for _, t := range a.targets[ni] {
			if lt, ok := wg.local[t]; ok {
				wg.g.AddEdge(wg.local[ni], lt)
			}
		}
	}
	wg.g.Normalize()
	return wg
}

func (a *aggregate) ids(nodes []int, wg *workspaceGraph) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = a.p.notes[wg.nodes[n]].ID
	}
	return out
}

func sortedIDs(ids []string) []string {
	out := append([]string{}, ids...)
	sort.Strings(out)
	return out
}

func communityOf(n *notePlan) string {
	if n.Topic < 0 {
		return "unlabelled"
	}
	return topicIDs[n.Topic]
}

// writeGraphTruth computes and writes the graph ground truth, and checks the
// planted structure is what the final text actually contains.
func (a *aggregate) writeGraphTruth(o *outputs) error {
	for _, w := range a.p.workspaces {
		wg := a.graphOf(w)
		g := wg.g
		sccs := g.SCC()
		planted := map[string]string{}
		for _, s := range w.SCCs {
			planted[strings.Join(sortedIDs(a.globalIDs(s.Members)), ",")] = s.Label
		}
		type scc struct{ ids []string }
		var nontrivial []scc
		largest, trivial := 0, 0
		for _, c := range sccs {
			largest = max(largest, len(c))
			if len(c) == 1 {
				if !g.HasEdge(c[0], c[0]) {
					trivial++
					continue
				}
			}
			nontrivial = append(nontrivial, scc{ids: sortedIDs(a.ids(c, wg))})
		}
		sort.Slice(nontrivial, func(i, j int) bool { return nontrivial[i].ids[0] < nontrivial[j].ids[0] })
		seen := map[string]bool{}
		for i, c := range nontrivial {
			key := strings.Join(c.ids, ",")
			rec := SCCRecord{WorkspaceID: w.ID, Index: i, Size: len(c.ids), Members: c.ids}
			if label, ok := planted[key]; ok {
				rec.Planted = &label
				seen[key] = true
			} else if w.Kind == "procedural" {
				return fmt.Errorf("workspace %s: unplanted SCC of size %d", w.Name, len(c.ids))
			}
			if err := o.write("truth/graph/sccs.jsonl", rec); err != nil {
				return err
			}
		}
		for key, label := range planted {
			if !seen[key] {
				return fmt.Errorf("workspace %s: planted %s is not an SCC of the final graph", w.Name, label)
			}
		}
		// Cycles: the planted ones, and one shortest cycle per SCC.
		for _, cyc := range w.Cycles {
			ids := a.globalIDs(cyc)
			for k := 0; k+1 < len(cyc); k++ {
				if !g.HasEdge(wg.local[cyc[k]], wg.local[cyc[k+1]]) {
					a1, b1 := a.p.notes[cyc[k]], a.p.notes[cyc[k+1]]
					return fmt.Errorf("workspace %s: planted cycle edge %q -> %q missing (planned links %v, final targets %v)",
						w.Name, a1.Title, b1.Title, a1.Links, a.targets[cyc[k]])
				}
			}
			if err := o.write("truth/graph/cycles.jsonl", CycleRecord{WorkspaceID: w.ID, Kind: "planted", Label: "planted", Cycle: ids}); err != nil {
				return err
			}
		}
		for i, c := range sccs {
			if len(c) < 2 {
				continue
			}
			cyc := shortestCycle(g, c)
			if err := o.write("truth/graph/cycles.jsonl", CycleRecord{WorkspaceID: w.ID, Kind: "shortest-through-min", Label: fmt.Sprintf("scc-%d", i), Cycle: a.ids(cyc, wg)}); err != nil {
				return err
			}
		}
		// Weak components and orphans.
		comps := g.WeakComponents()
		plantedOrphans := map[string]bool{}
		for _, og := range w.Orphans {
			plantedOrphans[strings.Join(sortedIDs(a.globalIDs(og)), ",")] = true
		}
		orphans := 0
		for i, c := range comps {
			ids := sortedIDs(a.ids(c, wg))
			hasRoot := false
			for _, v := range c {
				hasRoot = hasRoot || a.p.notes[wg.nodes[v]].Root
			}
			if !hasRoot {
				orphans++
			}
			rec := ComponentRecord{WorkspaceID: w.ID, Index: i, Size: len(c), HasRoot: hasRoot, Orphan: !hasRoot, Planted: plantedOrphans[strings.Join(ids, ",")], Members: ids}
			if err := o.write("truth/graph/components.jsonl", rec); err != nil {
				return err
			}
		}
		// Hubs by in- and out-degree.
		order := make([]int, g.N())
		for i := range order {
			order[i] = i
		}
		plantedHub := map[int]bool{}
		for _, h := range w.Hubs {
			plantedHub[h] = true
		}
		for _, by := range []string{"in_degree", "out_degree"} {
			deg := func(v int) int {
				if by == "in_degree" {
					return len(g.In[v])
				}
				return len(g.Out[v])
			}
			sort.SliceStable(order, func(i, j int) bool {
				if deg(order[i]) != deg(order[j]) {
					return deg(order[i]) > deg(order[j])
				}
				return a.p.notes[wg.nodes[order[i]]].ID < a.p.notes[wg.nodes[order[j]]].ID
			})
			for rank := 0; rank < min(10, len(order)); rank++ {
				v := order[rank]
				n := a.p.notes[wg.nodes[v]]
				if err := o.write("truth/graph/hubs.jsonl", HubRecord{WorkspaceID: w.ID, NoteID: n.ID, Title: n.Title, By: by, Rank: rank + 1,
					InDegree: len(g.In[v]), OutDegree: len(g.Out[v]), Planted: plantedHub[n.Index]}); err != nil {
					return err
				}
			}
		}
		if err := a.writeBlastAndPaths(o, wg); err != nil {
			return err
		}
		// Communities and the links between them.
		for _, ni := range wg.nodes {
			n := a.p.notes[ni]
			if err := o.write("truth/graph/communities.jsonl", CommunityRecord{WorkspaceID: w.ID, NoteID: n.ID, Community: communityOf(n)}); err != nil {
				return err
			}
		}
		for u := 0; u < g.N(); u++ {
			for _, v := range g.Out[u] {
				a1, b1 := a.p.notes[wg.nodes[u]], a.p.notes[wg.nodes[v]]
				if communityOf(a1) != communityOf(b1) {
					if err := o.write("truth/graph/bridges.jsonl", BridgeRecord{WorkspaceID: w.ID, From: a1.ID, To: b1.ID,
						FromCommunity: communityOf(a1), ToCommunity: communityOf(b1)}); err != nil {
						return err
					}
				}
			}
		}
		dangling := 0
		for _, d := range a.dangling[w.Index] {
			dangling++
			if err := o.write("truth/graph/dangling.jsonl", d); err != nil {
				return err
			}
		}
		if err := o.write("truth/graph/summary.jsonl", GraphSummaryRecord{WorkspaceID: w.ID, Nodes: g.N(), Edges: g.Edges(), NontrivialSCCs: len(nontrivial),
			TrivialSCCs: trivial, LargestSCC: largest, Components: len(comps), OrphanComponents: orphans, DanglingLinks: dangling, Acyclic: len(nontrivial) == 0}); err != nil {
			return err
		}
	}
	return nil
}

func (a *aggregate) globalIDs(idx []int) []string {
	out := make([]string, len(idx))
	for i, n := range idx {
		out[i] = a.p.notes[n].ID
	}
	return out
}

// shortestCycle returns a shortest directed cycle through the smallest
// member of an SCC (first equals last).
func shortestCycle(g *graphs.Graph, comp []int) []int {
	in := map[int]bool{}
	for _, v := range comp {
		in[v] = true
	}
	s := comp[0]
	parent := map[int]int{s: -1}
	queue := []int{s}
	for q := 0; q < len(queue); q++ {
		v := queue[q]
		for _, w := range g.Out[v] {
			if w == s {
				var path []int
				for x := v; x >= 0; x = parent[x] {
					path = append(path, x)
				}
				for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
					path[i], path[j] = path[j], path[i]
				}
				return append(path, s)
			}
			if _, ok := parent[w]; !ok && in[w] {
				parent[w] = v
				queue = append(queue, w)
			}
		}
	}
	return []int{s, s}
}

func (a *aggregate) writeBlastAndPaths(o *outputs, wg *workspaceGraph) error {
	w, g := wg.w, wg.g
	if g.N() == 0 {
		return nil
	}
	r := a.p.src.Stream("truth.graph", uint64(w.Index))
	type source struct {
		node int
		why  string
	}
	var sources []source
	addGlobal := func(ni int, why string) {
		if l, ok := wg.local[ni]; ok {
			sources = append(sources, source{l, why})
		}
	}
	for _, h := range w.Hubs {
		addGlobal(h, "planted hub")
	}
	if len(w.Chain) > 0 {
		addGlobal(w.Chain[0], "head of the planted chain")
	}
	for _, ni := range w.Notes {
		if a.p.notes[ni].Root {
			addGlobal(ni, "root note")
			break
		}
	}
	for _, s := range w.SCCs {
		addGlobal(s.Members[0], "member of planted "+s.Label)
		break
	}
	for len(sources) < a.p.prof.BlastSources+3 && len(sources) < g.N() {
		sources = append(sources, source{r.IntN(g.N()), "random note"})
	}
	for _, s := range sources {
		for _, forward := range []bool{true, false} {
			dist, rings := g.Rings(s.node, forward, 0)
			rec := BlastRecord{WorkspaceID: w.ID, NoteID: a.p.notes[wg.nodes[s.node]].ID, Direction: "inbound", RingSizes: rings, Why: s.why}
			if forward {
				rec.Direction = "outbound"
			}
			for _, x := range rings {
				rec.Reached += x
			}
			if rec.Reached <= maxListed {
				rec.Hops = map[string]int{}
				for v, d := range dist {
					if d >= 0 {
						rec.Hops[a.p.notes[wg.nodes[v]].ID] = d
					}
				}
			} else {
				rec.Truncated = true
			}
			if err := o.write("truth/graph/blast_radius.jsonl", rec); err != nil {
				return err
			}
		}
	}
	type pair struct{ a, b int }
	var pairs []pair
	if len(w.Chain) > 1 {
		pairs = append(pairs, pair{wg.local[w.Chain[0]], wg.local[w.Chain[len(w.Chain)-1]]})
	}
	if len(w.Orphans) > 0 {
		if l, ok := wg.local[w.Orphans[len(w.Orphans)-1][0]]; ok {
			pairs = append(pairs, pair{0, l})
		}
	}
	for len(pairs) < a.p.prof.PathPairs+2 && g.N() > 1 {
		pairs = append(pairs, pair{r.IntN(g.N()), r.IntN(g.N())})
	}
	for _, pr := range pairs {
		path, count := g.ShortestPath(pr.a, pr.b)
		rec := PathRecord{WorkspaceID: w.ID, From: a.p.notes[wg.nodes[pr.a]].ID, To: a.p.notes[wg.nodes[pr.b]].ID, Metric: "PATH_METRIC_HOPS",
			Found: path != nil, Distance: -1, PathCount: count, Unique: count == 1}
		if path != nil {
			rec.Distance = len(path) - 1
			if len(path) <= maxListed {
				rec.Path = a.ids(path, wg)
			} else {
				rec.Truncated = true
			}
		}
		if err := o.write("truth/graph/shortest_paths.jsonl", rec); err != nil {
			return err
		}
	}
	return nil
}

// writeSearchTruth writes the text, typo, regex and title truth.
func (a *aggregate) writeSearchTruth(o *outputs) error {
	for _, w := range a.p.workspaces {
		r := a.p.src.Stream("truth.search", uint64(w.Index))
		for qi, q := range w.Queries {
			qrels := append([]Qrel{}, a.qrels[w.Index][qi]...)
			sort.Slice(qrels, func(i, j int) bool {
				if qrels[i].Grade != qrels[j].Grade {
					return qrels[i].Grade > qrels[j].Grade
				}
				return qrels[i].NoteID < qrels[j].NoteID
			})
			subject := a.p.users[w.Owner].ID
			if q.Subject >= 0 {
				subject = a.p.users[q.Subject].ID
			}
			if err := o.write("truth/search/text_queries.jsonl", TextQueryRecord{ID: q.ID, WorkspaceID: w.ID, Query: q.Text, Terms: q.Terms,
				Kind: q.Kind, SubjectUserID: subject, Qrels: qrels}); err != nil {
				return err
			}
			if (q.Kind == "codename" || q.Kind == "anchor") && len(q.Terms) == 1 && utf8.RuneCountInString(q.Terms[0]) >= 5 && strings.IndexFunc(q.Terms[0], isCJK) < 0 {
				typo, dist := typoOf(r, q.Terms[0])
				var targets []string
				for _, qr := range qrels {
					targets = append(targets, qr.NoteID)
				}
				sort.Strings(targets)
				if targets == nil {
					targets = []string{}
				}
				if err := o.write("truth/search/typo_queries.jsonl", TypoQueryRecord{ID: q.ID + "-typo", WorkspaceID: w.ID, Query: typo, Intended: q.Terms[0],
					EditDistance: dist, TargetQueryID: q.ID, TargetNoteIDs: targets}); err != nil {
					return err
				}
			}
		}
		for qi, q := range w.Regexes {
			ms := a.matches[w.Index][qi]
			if ms == nil {
				ms = []regexMatch{}
			}
			langs := q.Languages
			if langs == nil {
				langs = []string{}
			}
			if err := o.write("truth/search/regex_queries.jsonl", RegexQueryRecord{ID: q.ID, WorkspaceID: w.ID, Pattern: q.Pattern, CaseSensitive: q.CaseSensitive,
				CodeOnly: q.CodeOnly, Languages: langs, MatchCount: len(ms), Matches: ms}); err != nil {
				return err
			}
		}
		if err := a.writeTitlePrefixes(o, w, r); err != nil {
			return err
		}
	}
	return nil
}

func (a *aggregate) writeTitlePrefixes(o *outputs, w *wsPlan, r *prng.Rand) error {
	var live []*notePlan
	for _, ni := range w.Notes {
		if !a.p.notes[ni].Trashed {
			live = append(live, a.p.notes[ni])
		}
	}
	if len(live) == 0 {
		return nil
	}
	expected := func(prefix string) []TitleMatch {
		lp := strings.ToLower(prefix)
		out := []TitleMatch{}
		for _, n := range live {
			if strings.HasPrefix(strings.ToLower(n.Title), lp) {
				out = append(out, TitleMatch{NoteID: n.ID, Title: n.Title})
			}
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Title != out[j].Title {
				return out[i].Title < out[j].Title
			}
			return out[i].NoteID < out[j].NoteID
		})
		if len(out) > maxListed {
			out = out[:maxListed]
		}
		return out
	}
	seen := map[string]bool{}
	for k := 0; k < 12; k++ {
		n := live[r.IntN(len(live))]
		rs := []rune(strings.ToLower(n.Title))
		l := min(len(rs), 3+r.IntN(6))
		prefix := string(rs[:l])
		if seen[prefix] {
			continue
		}
		seen[prefix] = true
		if err := o.write("truth/search/title_prefixes.jsonl", TitlePrefixRecord{WorkspaceID: w.ID, Prefix: prefix, Expected: expected(prefix)}); err != nil {
			return err
		}
	}
	for k := 0; k < 4; k++ {
		n := live[r.IntN(len(live))]
		rs := []rune(n.Title)
		l := min(len(rs), 6+r.IntN(5))
		orig := string(rs[:l])
		typo, _ := typoOf(r, orig)
		id := n.ID
		if err := o.write("truth/search/title_prefixes.jsonl", TitlePrefixRecord{WorkspaceID: w.ID, Prefix: typo, Typo: true, Expected: expected(typo),
			IntendedNoteID: &id, EditDistance: levenshtein(strings.ToLower(typo), strings.ToLower(orig))}); err != nil {
			return err
		}
	}
	return nil
}

func vec64(v [hashembed.Dim]float32) [hashembed.Dim]float64 {
	var out [hashembed.Dim]float64
	for i, x := range v {
		out[i] = float64(x)
	}
	return out
}

// rankAmong returns b's 1-based rank among a's workspace neighbours by
// hashed cosine (ties broken by note ID).
func (a *aggregate) rankAmong(w *wsPlan, from, target int) int {
	va := vec64(a.vecs[from])
	vt := hashembed.Cosine(va, vec64(a.vecs[target]))
	rank := 1
	for _, ni := range w.Notes {
		if ni == from || ni == target || a.p.notes[ni].Trashed {
			continue
		}
		c := hashembed.Cosine(va, vec64(a.vecs[ni]))
		if c > vt || (c == vt && a.p.notes[ni].ID < a.p.notes[target].ID) {
			rank++
		}
	}
	return rank
}

func (a *aggregate) linked(x, y int) bool {
	for _, t := range a.targets[x] {
		if t == y {
			return true
		}
	}
	for _, t := range a.targets[y] {
		if t == x {
			return true
		}
	}
	return false
}

func (a *aggregate) writeDiscoveryTruth(o *outputs) error {
	for _, w := range a.p.workspaces {
		for _, pr := range w.TwinPairs {
			x, y := pr[0], pr[1]
			if a.linked(x, y) {
				return fmt.Errorf("twin notes %d and %d ended up linked", x, y)
			}
			cos := hashembed.Cosine(vec64(a.vecs[x]), vec64(a.vecs[y]))
			if err := o.write("truth/discovery/suggested_links.jsonl", SuggestedLinkRecord{WorkspaceID: w.ID, A: a.p.notes[x].ID, B: a.p.notes[y].ID,
				SharedTerms: a.p.notes[x].TwinTerms, Cosine: hashembed.Round4(cos), RankBForA: a.rankAmong(w, x, y), RankAForB: a.rankAmong(w, y, x)}); err != nil {
				return err
			}
		}
		for _, ni := range w.Notes {
			n := a.p.notes[ni]
			if n.DupOf < 0 {
				continue
			}
			j := jaccard(a.shingles[n.DupOf], a.shingles[ni])
			cos := hashembed.Cosine(vec64(a.vecs[n.DupOf]), vec64(a.vecs[ni]))
			if err := o.write("truth/discovery/near_duplicates.jsonl", NearDuplicateRecord{WorkspaceID: w.ID, Original: a.p.notes[n.DupOf].ID, Duplicate: n.ID,
				Jaccard: hashembed.Round4(j), Cosine: hashembed.Round4(cos), Edits: "one block dropped, up to three words changed, one sentence added"}); err != nil {
				return err
			}
		}
	}
	return nil
}

func jaccard(a, b []uint64) float64 {
	i, j, inter := 0, 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			inter++
			i, j = i+1, j+1
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func (a *aggregate) writeClusteringTruth(o *outputs) error {
	for _, n := range a.p.notes {
		rec := NoteTopicRecord{NoteID: n.ID, WorkspaceID: a.p.ws(n).ID, Mix: hashembed.Round4(n.Mix), Kind: n.Kind}
		if n.Topic >= 0 {
			rec.Topic = &topicIDs[n.Topic]
		}
		if n.Topic2 >= 0 {
			rec.SecondaryTopic = &topicIDs[n.Topic2]
		}
		if err := o.write("truth/clustering/note_topics.jsonl", rec); err != nil {
			return err
		}
	}
	type topicDoc struct {
		ID    string   `json:"id"`
		Name  string   `json:"name"`
		Vocab []string `json:"vocab"`
	}
	doc := struct {
		Topics   []topicDoc `json:"topics"`
		Embedder struct {
			Name      string   `json:"name"`
			Dim       int      `json:"dim"`
			Note      string   `json:"note"`
			Stopwords []string `json:"stopwords"`
		} `json:"embedder"`
	}{}
	for _, t := range corpus.Topics {
		doc.Topics = append(doc.Topics, topicDoc{ID: t.ID, Name: t.Name, Vocab: t.Vocab})
	}
	doc.Embedder.Name, doc.Embedder.Dim = hashembed.Name, hashembed.Dim
	doc.Embedder.Note = "Deterministic feature hashing of word tokens (algorithm in docs/seed-data.md). A stand-in with real topic signal; real embeddings come from the ml service."
	doc.Embedder.Stopwords = hashembed.Stopwords()
	return o.document("truth/clustering/topics.json", doc)
}

// writePermissionTruth evaluates a bounded matrix per workspace that covers
// every role, override, share link state and removal.
func (a *aggregate) writePermissionTruth(o *outputs) error {
	p := a.p
	at := ts(ReferenceTime)
	linksByWS := map[int][]shareLinkPlan{}
	for _, l := range p.links {
		ws := p.notes[l.Note].WS
		linksByWS[ws] = append(linksByWS[ws], l)
	}
	overByWS := map[int][]overridePlan{}
	for _, ov := range p.overrides {
		ws := p.notes[ov.Note].WS
		overByWS[ws] = append(overByWS[ws], ov)
	}
	for _, w := range p.workspaces {
		r := p.src.Stream("truth.permissions", uint64(w.Index))
		// Subjects: owner, admins, removed members, guests, override
		// holders, then other members, capped; two outsiders; an agent run
		// for one member and one guest.
		var users []int
		add := func(u int) {
			if !containsInt(users, u) && len(users) < p.prof.MatrixSubjects {
				users = append(users, u)
			}
		}
		for _, role := range []policy.WorkspaceRole{policy.Owner, policy.Admin} {
			for _, m := range w.Members {
				if m.Role == role {
					add(m.User)
				}
			}
		}
		for _, m := range w.Members {
			if m.RemovedAt != nil || m.Role == policy.Guest {
				add(m.User)
			}
		}
		for _, ov := range overByWS[w.Index] {
			add(ov.User)
		}
		for _, m := range w.Members {
			add(m.User)
		}
		var outsiders []int
		for u := len(p.users) - 1; u >= 0 && len(outsiders) < 2; u-- {
			if p.policy.WorkspaceRoleAt(w.ID, p.users[u].ID, ReferenceTime) == 0 && !p.policy.WasEverMember(w.ID, p.users[u].ID) {
				outsiders = append(outsiders, u)
			}
		}
		var subjects []MatrixSubject
		for _, u := range append(users, outsiders...) {
			subjects = append(subjects, MatrixSubject{UserID: &p.users[u].ID})
		}
		for k, u := range users {
			if k == 2 || (k > 2 && p.policy.WorkspaceRoleAt(w.ID, p.users[u].ID, ReferenceTime) == policy.Guest) {
				run := p.newID("matrix-run", w.Index*100+k, ReferenceTime.Add(-time.Hour))
				subjects = append(subjects, MatrixSubject{UserID: &p.users[u].ID, AgentRunID: &run})
				if k > 2 {
					break
				}
			}
		}
		for i := range linksByWS[w.Index] {
			l := linksByWS[w.Index][i]
			subjects = append(subjects, MatrixSubject{ShareLinkID: &l.ID})
		}
		// Resources: notes with overrides or links, trashed notes, then a
		// random sample.
		var notes []int
		addNote := func(ni int) {
			if !containsInt(notes, ni) && len(notes) < p.prof.MatrixNotes {
				notes = append(notes, ni)
			}
		}
		for _, ov := range overByWS[w.Index] {
			addNote(ov.Note)
		}
		for _, l := range linksByWS[w.Index] {
			addNote(l.Note)
		}
		for _, ni := range w.Notes {
			if p.notes[ni].Trashed {
				addNote(ni)
			}
		}
		for _, k := range r.Sample(len(w.Notes), min(len(w.Notes), p.prof.MatrixNotes)) {
			addNote(w.Notes[k])
		}
		write := func(s MatrixSubject, act policy.Action, res string) error {
			subj := policy.Subject{}
			if s.UserID != nil {
				subj.UserID = *s.UserID
			}
			if s.AgentRunID != nil {
				subj.AgentRunID = *s.AgentRunID
			}
			if s.ShareLinkID != nil {
				subj.ShareLinkID = *s.ShareLinkID
			}
			d := p.policy.Evaluate(subj, act, res, ReferenceTime)
			exp := "deny"
			if d.Allowed {
				exp = "allow"
			}
			return o.write("truth/permissions/matrix.jsonl", PermissionRecord{WorkspaceID: w.ID, Subject: s, Action: string(act), ResourceID: res, At: at, Expected: exp, Rule: d.Rule})
		}
		for _, s := range subjects {
			for _, act := range policy.WorkspaceActions {
				if err := write(s, act, w.ID); err != nil {
					return err
				}
			}
			for _, ni := range notes {
				if s.ShareLinkID != nil && p.linkNote(*s.ShareLinkID) != ni && r.Chance(0.8) {
					continue // a few cross-note checks per link are enough
				}
				for _, act := range policy.NoteActions {
					if err := write(s, act, p.notes[ni].ID); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (p *plan) linkNote(id string) int {
	for _, l := range p.links {
		if l.ID == id {
			return l.Note
		}
	}
	return -1
}

// resolveEvals turns the anchor evals into records with note and block IDs.
func (a *aggregate) resolveEvals() ([]EvalRecord, error) {
	p := a.p
	w := p.workspaces[0]
	cast := map[string]int{}
	for i, c := range corpus.AnchorCast {
		cast[c.Key] = i
	}
	byTitle := func(t string) (int, error) {
		idx, ok := w.TitleIndex[t]
		if !ok || idx < 0 {
			return 0, fmt.Errorf("eval: no unique anchor note titled %q", t)
		}
		return idx, nil
	}
	var out []EvalRecord
	for _, e := range anchor.Evals {
		u := cast[e.AsUser]
		rec := EvalRecord{ID: e.ID, WorkspaceID: w.ID, UserID: p.users[u].ID, CastKey: e.AsUser, Prompt: e.Prompt, Outcome: e.Outcome,
			MustInclude: orEmpty(e.MustInclude), MustNotInclude: orEmpty(e.MustNotInclude), ExpectedTools: orEmpty(e.ExpectedTools),
			ForbiddenTools: orEmpty(e.ForbiddenTools), Citations: []EvalCitation{}, Why: e.Why}
		for _, c := range e.Citations {
			ni, err := byTitle(c.Title)
			if err != nil {
				return nil, err
			}
			found := false
			for _, b := range a.kept[ni] {
				if k := strings.Index(b.Block.Text, c.Snippet); k >= 0 {
					if found {
						return nil, fmt.Errorf("eval %s: snippet %q is in two blocks", e.ID, c.Snippet)
					}
					start := utf8.RuneCountInString(b.Block.Text[:k])
					rec.Citations = append(rec.Citations, EvalCitation{NoteID: p.notes[ni].ID, BlockID: b.ID, Start: start,
						End: start + utf8.RuneCountInString(c.Snippet), Quote: c.Snippet})
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("eval %s: snippet %q not found in %q", e.ID, c.Snippet, c.Title)
			}
		}
		if e.Confirmation != nil {
			conf := &EvalConfirmation{Tool: e.Confirmation.Tool}
			for _, t := range e.Confirmation.Targets {
				ni, err := byTitle(t)
				if err != nil {
					return nil, err
				}
				conf.TargetNoteIDs = append(conf.TargetNoteIDs, p.notes[ni].ID)
			}
			rec.Confirmation = conf
		}
		// The bulk-delete target list must be exactly the notes with the tag.
		if e.ID == "bulk-delete-drafts" {
			var want []string
			for _, ni := range w.Notes {
				if containsString(p.notes[ni].Tags, "draft") && !p.notes[ni].Trashed {
					want = append(want, p.notes[ni].ID)
				}
			}
			got := append([]string{}, rec.Confirmation.TargetNoteIDs...)
			sort.Strings(want)
			sort.Strings(got)
			if strings.Join(want, ",") != strings.Join(got, ",") {
				return nil, fmt.Errorf("eval %s: targets differ from the notes tagged draft", e.ID)
			}
		}
		if err := a.checkEvalArguments(e, byTitle); err != nil {
			return nil, err
		}
		// Graph evals list backlinks; check they really link.
		if containsString(e.ExpectedTools, "blast_radius") {
			target, _ := byTitle(e.Subject)
			for _, t := range e.MustInclude {
				src, err := byTitle(t)
				if err != nil {
					return nil, err
				}
				if !containsInt(a.targets[src], target) {
					return nil, fmt.Errorf("eval %s: %q does not link to %q", e.ID, t, e.Subject)
				}
			}
		}
		out = append(out, rec)
	}
	return out, nil
}

// checkEvalArguments fails if an eval's transcript arguments would not work
// against the real tools: an unknown subject, an edit whose find text is not
// in the note exactly once, or a new note linking to a missing title.
func (a *aggregate) checkEvalArguments(e anchor.Eval, byTitle func(string) (int, error)) error {
	for _, tool := range append(append([]string{}, e.ExpectedTools...), e.ForbiddenTools...) {
		if !containsString(anchor.AgentTools, tool) {
			return fmt.Errorf("eval %s: %q is not an agent tool", e.ID, tool)
		}
	}
	if e.Confirmation != nil && !containsString(anchor.AgentTools, e.Confirmation.Tool) {
		return fmt.Errorf("eval %s: %q is not an agent tool", e.ID, e.Confirmation.Tool)
	}
	needsSubject := containsString(e.ExpectedTools, "blast_radius") || containsString(e.ExpectedTools, "apply_edit")
	if needsSubject != (e.Subject != "") {
		return fmt.Errorf("eval %s: a subject is needed exactly when blast_radius or apply_edit is expected", e.ID)
	}
	if containsString(e.ExpectedTools, "apply_edit") != (e.Edit != nil) {
		return fmt.Errorf("eval %s: an edit is needed exactly when apply_edit is expected", e.ID)
	}
	if containsString(e.ExpectedTools, "create_note") != (e.NewNote != nil) {
		return fmt.Errorf("eval %s: a new note is needed exactly when create_note is expected", e.ID)
	}
	if e.Subject != "" {
		ni, err := byTitle(e.Subject)
		if err != nil {
			return err
		}
		if e.Edit != nil {
			var text strings.Builder
			for _, b := range a.kept[ni] {
				text.WriteString(b.Block.Text)
				text.WriteString("\n")
			}
			if n := strings.Count(text.String(), e.Edit.Find); n != 1 {
				return fmt.Errorf("eval %s: edit text occurs %d times in %q, want once", e.ID, n, e.Subject)
			}
		}
	}
	if e.NewNote != nil {
		for _, t := range e.NewNote.Links {
			if _, err := byTitle(t); err != nil {
				return err
			}
		}
	}
	return nil
}

func orEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}
