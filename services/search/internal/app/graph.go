package app

import (
	"context"
	"log/slog"
	"math"
	"slices"
	"strings"

	"github.com/genuinebnt/skein-go/libs/dp"
	"github.com/genuinebnt/skein-go/libs/graph"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
)

// Grapher implements GraphService over the link graph projection. Every
// graph it builds holds only the caller's readable, visible notes, so no
// result (not even a count or a ring size) can reveal another note.
type Grapher struct {
	index      *Index
	access     *Access
	analyzer   *Analyzer
	similarity Similarity // nil: semantic paths treat every link as cosine 0
	relevance  Relevance  // nil: reading plans use lexical relevance
	logger     *slog.Logger
}

// NewGrapher returns a Grapher. similarity and relevance may be nil.
func NewGrapher(index *Index, access *Access, analyzer *Analyzer, similarity Similarity, relevance Relevance, logger *slog.Logger) *Grapher {
	if logger == nil {
		logger = logging.Discard()
	}
	return &Grapher{index: index, access: access, analyzer: analyzer, similarity: similarity, relevance: relevance, logger: logger}
}

// GraphNode is a node of GetLinkGraph.
type GraphNode struct {
	ID, Title string
	Root      bool
}

// LinkGraph is GetLinkGraph's result.
type LinkGraph struct {
	Nodes []GraphNode
	Edges []graph.Edge
}

// view is a readable graph and what describes its notes.
type view struct {
	workspace string
	g         *graph.Graph
	version   int64
	titles    map[string]string
	readable  domain.Readable
}

// workspaceView builds the subject's readable graph of a workspace.
func (s *Grapher) workspaceView(ctx context.Context, subject auth.Subject, workspace string) (view, error) {
	if workspace == "" {
		return view{}, invalid("workspace_id", "is required")
	}
	readable, err := s.access.Readable(ctx, subject, workspace)
	if err != nil {
		return view{}, err
	}
	v := view{workspace: workspace, readable: readable, titles: map[string]string{}}
	err = s.index.Read(func(p *domain.Projection) error {
		w, ok := p.Workspace(workspace)
		if !ok {
			v.g, _ = graph.New(nil, nil)
			return nil
		}
		v.version = w.GraphVersion
		g, err := w.LinkGraph(readable.Has)
		if err != nil {
			return err
		}
		v.g = g
		for _, id := range g.Nodes() {
			n, _ := w.Note(id)
			v.titles[id] = n.Title
		}
		return nil
	})
	return v, err
}

// noteView resolves a note's workspace and builds the subject's readable
// graph of it. A note that does not exist, is not visible, or that the
// subject cannot read is NOT_FOUND.
func (s *Grapher) noteView(ctx context.Context, subject auth.Subject, note, field string) (view, error) {
	if note == "" {
		return view{}, invalid(field, "is required")
	}
	var workspace string
	_ = s.index.Read(func(p *domain.Projection) error {
		if w, ok := p.WorkspaceOf(note); ok {
			if n, _ := w.Note(note); n.Visible() {
				workspace = w.ID
			}
		}
		return nil
	})
	if workspace == "" {
		return view{}, noteNotFound(note)
	}
	v, err := s.workspaceView(ctx, subject, workspace)
	if err != nil {
		if IsNotFound(err) {
			return view{}, noteNotFound(note)
		}
		return view{}, err
	}
	if !v.g.Has(note) {
		return view{}, noteNotFound(note)
	}
	return v, nil
}

// GetLinkGraph returns the subject's readable link graph of a workspace.
func (s *Grapher) GetLinkGraph(ctx context.Context, subject auth.Subject, workspace string) (LinkGraph, error) {
	v, err := s.workspaceView(ctx, subject, workspace)
	if err != nil {
		return LinkGraph{}, err
	}
	out := LinkGraph{Edges: v.g.Edges()}
	roots := map[string]bool{}
	for _, r := range v.g.Roots() {
		roots[r] = true
	}
	for _, id := range v.g.Nodes() {
		out.Nodes = append(out.Nodes, GraphNode{ID: id, Title: v.titles[id], Root: roots[id]})
	}
	return out, nil
}

// AnalyzeGraph runs the parameter-free algorithms over the readable graph.
func (s *Grapher) AnalyzeGraph(ctx context.Context, subject auth.Subject, workspace string) (Analysis, error) {
	v, err := s.workspaceView(ctx, subject, workspace)
	if err != nil {
		return Analysis{}, err
	}
	return s.analyzer.Analyze(GraphKey(workspace, v.g), v.version, v.g)
}

// BlastRadius returns the notes within maxHops of note following links in
// dir (graph.CitedBy by default).
func (s *Grapher) BlastRadius(ctx context.Context, subject auth.Subject, note string, maxHops int, dir graph.Direction) (graph.Rings, error) {
	v, err := s.noteView(ctx, subject, note, "note_id")
	if err != nil {
		return graph.Rings{}, err
	}
	return v.g.BlastRadius(note, dir, maxHops)
}

// ShortestPath finds the cheapest path between two notes: by hops, or by
// semantic distance (1 - cosine of the notes' embeddings, from ml) along
// links. Notes in different workspaces have no path; an unreadable note is
// NOT_FOUND.
func (s *Grapher) ShortestPath(ctx context.Context, subject auth.Subject, from, to string, semantic bool) (graph.Path, error) {
	v, err := s.noteView(ctx, subject, from, "from")
	if err != nil {
		return graph.Path{}, err
	}
	if !v.g.Has(to) {
		if _, err := s.noteView(ctx, subject, to, "to"); err != nil {
			return graph.Path{}, err
		}
		return graph.Path{Nodes: []string{}}, nil // readable, but in another workspace
	}
	if !semantic {
		return v.g.ShortestPath(from, to)
	}
	weighted, err := s.semanticGraph(ctx, v)
	if err != nil {
		return graph.Path{}, err
	}
	return weighted.ShortestWeightedPath(from, to)
}

// semanticGraph rebuilds v's graph with each link weighted by semantic
// distance. Links ml knows nothing about weigh 1, the distance of
// unrelated notes (cosine 0).
func (s *Grapher) semanticGraph(ctx context.Context, v view) (*graph.Graph, error) {
	edges := v.g.Edges()
	cosines := map[NotePair]float64{}
	if s.similarity != nil {
		pairs := make([]NotePair, 0, len(edges))
		for _, e := range edges {
			pairs = append(pairs, pairOf(e.From, e.To))
		}
		var err error
		if cosines, err = s.similarity.NoteCosines(ctx, v.workspace, pairs); err != nil {
			return nil, err
		}
	}
	for i, e := range edges {
		weight := 1.0
		if c, ok := cosines[pairOf(e.From, e.To)]; ok {
			d, err := graph.SemanticDistance(c)
			if err != nil {
				return nil, err
			}
			weight = d
		}
		edges[i].Weight = weight
	}
	nodes := make([]graph.Node, 0, v.g.Len())
	for _, id := range v.g.Nodes() {
		nodes = append(nodes, graph.Node{ID: id})
	}
	return graph.New(nodes, edges)
}

func pairOf(a, b string) NotePair {
	if b < a {
		a, b = b, a
	}
	return NotePair{From: a, To: b}
}

// ReadingStep is a step of ReadingPath.
type ReadingStep struct {
	Note   string
	Reason string
	Depth  int
}

// ReadingPath lists what to read before note, prerequisites first: the
// notes it links to, directly or transitively, as libs/graph orders them.
// The target itself is not a step.
func (s *Grapher) ReadingPath(ctx context.Context, subject auth.Subject, note string) ([]ReadingStep, error) {
	v, err := s.noteView(ctx, subject, note, "note_id")
	if err != nil {
		return nil, err
	}
	reading, err := v.g.ReadingPath(note)
	if err != nil {
		return nil, err
	}
	inPath := map[string]bool{}
	for _, st := range reading.Steps {
		inPath[st.Note] = true
	}
	cycleOf := map[string][]string{}
	for _, c := range reading.Cycles {
		for _, id := range c {
			cycleOf[id] = c
		}
	}
	steps := []ReadingStep{}
	for _, st := range reading.Steps {
		if st.Note == note {
			continue
		}
		var from []string
		citers, _ := v.g.Neighbours(st.Note, graph.CitedBy)
		for _, c := range citers {
			if inPath[c] {
				from = append(from, v.titles[c])
			}
		}
		reason := "linked from " + joinNames(from)
		if c := cycleOf[st.Note]; len(c) > 0 {
			var others []string
			for _, id := range c {
				if id != st.Note {
					others = append(others, v.titles[id])
				}
			}
			reason += "; read together with " + joinNames(others) + ", which link to each other"
		}
		steps = append(steps, ReadingStep{Note: st.Note, Reason: reason, Depth: st.Hops})
	}
	return steps, nil
}

// joinNames writes "A", "A and B", "A, B and C".
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// PlannedNote is one note of a reading plan.
type PlannedNote struct {
	Note    string
	Minutes int
	Value   float64
	Reason  string
}

// ReadingPlanResult is ReadingPlan's result.
type ReadingPlanResult struct {
	Notes        []PlannedNote
	TotalMinutes int
	TotalValue   float64
}

// Reading plan parameters.
const (
	// WordsPerMinute estimates reading time.
	WordsPerMinute = 200
	// maxPlanCandidates keeps the knapsack small: the most relevant notes
	// only.
	maxPlanCandidates = 40
	// valueScale quantises relevance in [0, 1] to integers, as libs/dp
	// requires so that sums and ties are exact in every language.
	valueScale = 1000
)

// ReadingPlan chooses the most valuable notes on topic that fit in minutes
// of reading, every chosen note's prerequisites (the notes it links to)
// included and placed first: a precedence-constrained knapsack solved by
// libs/dp. Relevance comes from ml's semantic search, or, when ml is not
// configured or fails, from lexical BM25 scores normalized to [0, 1].
// Links inside a cycle cannot all be prerequisites, so only links between
// different strongly connected components count.
func (s *Grapher) ReadingPlan(ctx context.Context, subject auth.Subject, workspace, topic string, minutes int) (ReadingPlanResult, error) {
	switch {
	case strings.TrimSpace(topic) == "":
		return ReadingPlanResult{}, invalid("topic", "is required")
	case minutes <= 0:
		return ReadingPlanResult{}, invalid("minutes", "must be positive")
	case minutes > dp.MaxPlanMinutes:
		return ReadingPlanResult{}, invalid("minutes", "must be at most %d", dp.MaxPlanMinutes)
	}
	v, err := s.workspaceView(ctx, subject, workspace)
	if err != nil {
		return ReadingPlanResult{}, err
	}
	relevance, err := s.topicRelevance(ctx, v, topic)
	if err != nil {
		return ReadingPlanResult{}, err
	}
	candidates := topCandidates(relevance, v)
	notes, err := s.planInput(v, candidates, relevance)
	if err != nil {
		return ReadingPlanResult{}, err
	}
	plan, err := dp.PlanReading(notes, minutes, dp.PlanOptions{})
	if err != nil {
		return ReadingPlanResult{}, err
	}
	return s.describePlan(v, plan, notes), nil
}

func (s *Grapher) topicRelevance(ctx context.Context, v view, topic string) (map[string]float64, error) {
	if s.relevance != nil {
		scores, err := s.relevance.TopicRelevance(ctx, v.workspace, topic)
		if err == nil {
			return scores, nil
		}
		s.logger.WarnContext(ctx, "ml relevance unavailable, using lexical relevance", "error", err)
	}
	scores := map[string]float64{}
	err := s.index.Read(func(p *domain.Projection) error {
		w, ok := p.Workspace(v.workspace)
		if !ok {
			return nil
		}
		for _, term := range domain.ParseTextQuery(topic, false) {
			res, err := w.SearchText(term.Text, v.readable, domain.Filters{})
			if err != nil {
				continue
			}
			for _, m := range res.Matches {
				scores[m.Note] += m.Score
			}
		}
		return nil
	})
	top := 0.0
	for _, sc := range scores {
		top = max(top, sc)
	}
	for id, sc := range scores {
		scores[id] = sc / top
	}
	return scores, err
}

// topCandidates returns the readable notes with positive relevance, most
// relevant first (ties by ID), at most maxPlanCandidates.
func topCandidates(relevance map[string]float64, v view) []string {
	var out []string
	for id, r := range relevance {
		if r > 0 && v.g.Has(id) {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, func(a, b string) int {
		if relevance[a] != relevance[b] {
			if relevance[a] > relevance[b] {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	return out[:min(len(out), maxPlanCandidates)]
}

func (s *Grapher) planInput(v view, candidates []string, relevance map[string]float64) ([]dp.Note, error) {
	in := map[string]bool{}
	nodes := make([]graph.Node, 0, len(candidates))
	for _, id := range candidates {
		in[id] = true
		nodes = append(nodes, graph.Node{ID: id})
	}
	var edges []graph.Edge
	for _, e := range v.g.Edges() {
		if in[e.From] && in[e.To] {
			edges = append(edges, e)
		}
	}
	sub, err := graph.New(nodes, edges)
	if err != nil {
		return nil, err
	}
	scc := sub.StronglyConnected()
	lengths := map[string]int{}
	err = s.index.Read(func(p *domain.Projection) error {
		w, _ := p.Workspace(v.workspace)
		for _, id := range candidates {
			l := w.Words().Lengths(id)
			lengths[id] = l.Title + l.Body
		}
		return nil
	})
	notes := make([]dp.Note, 0, len(candidates))
	for _, id := range candidates {
		n := dp.Note{ID: id, Minutes: max(1, int(math.Ceil(float64(lengths[id])/WordsPerMinute))),
			Value: int64(math.Round(math.Min(1, relevance[id]) * valueScale))}
		targets, _ := sub.Neighbours(id, graph.Cites)
		for _, t := range targets {
			if scc.Of[t] != scc.Of[id] {
				n.Prerequisites = append(n.Prerequisites, t)
			}
		}
		notes = append(notes, n)
	}
	return notes, err
}

func (s *Grapher) describePlan(v view, plan dp.Plan, notes []dp.Note) ReadingPlanResult {
	prereqs := map[string][]string{}
	for _, n := range notes {
		prereqs[n.ID] = n.Prerequisites
	}
	chosen := map[string]bool{}
	var top int64 = -1
	for _, n := range plan.Notes {
		chosen[n.ID] = true
		top = max(top, n.Value)
	}
	out := ReadingPlanResult{TotalMinutes: plan.TotalMinutes, TotalValue: float64(plan.TotalValue) / valueScale}
	for _, n := range plan.Notes {
		var reason string
		switch {
		case n.Value == top && n.Value > 0:
			reason = "most relevant to the topic"
		case n.Value > 0:
			reason = "relevant to the topic"
		default:
			var by []string
			for _, r := range n.RequiredBy {
				by = append(by, v.titles[r])
			}
			reason = "needed before " + joinNames(by)
		}
		var needs []string
		for _, p := range prereqs[n.ID] {
			if chosen[p] {
				needs = append(needs, v.titles[p])
			}
		}
		if len(needs) > 0 {
			reason += "; needs " + joinNames(needs) + " first"
		}
		out.Notes = append(out.Notes, PlannedNote{Note: n.ID, Minutes: n.Minutes, Value: float64(n.Value) / valueScale, Reason: reason})
	}
	return out
}
