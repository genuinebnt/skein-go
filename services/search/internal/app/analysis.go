package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"

	"github.com/genuinebnt/skein-go/libs/graph"
	"github.com/genuinebnt/skein-go/platform/logging"
)

// Analysis is AnalyzeGraph's result.
type Analysis struct {
	ComponentOf map[string]int
	Orphans     []int
	SCCOf       map[string]int
	SCCSizes    []int
	Cycle       []string
	Topological []string
	Unplaced    []string
	PageRank    map[string]float64
	CommunityOf map[string]int
	Modularity  float64

	// Betweenness and Diameter are the slow metrics, computed as of
	// SlowVersion, which may trail GraphVersion while a newer computation
	// runs in the background (SlowPending). Betweenness then covers only
	// notes that still exist and are readable.
	Betweenness  map[string]float64
	Diameter     int
	GraphVersion int64
	SlowVersion  int64
	SlowPending  bool
}

// AnalyzerConfig tunes the analysis cache.
type AnalyzerConfig struct {
	// InlineLimit is the largest graph (in notes) whose slow metrics are
	// computed during the request. Betweenness and diameter both cost
	// O(V·(V+E)); at 2,000 notes that is tens of milliseconds, beyond it the
	// request would wait seconds. Default 2000.
	InlineLimit int
	// MaxEntries bounds the cache; the oldest entry is evicted. Default 256.
	MaxEntries int
	Logger     *slog.Logger
}

// Analyzer computes and caches graph analyses per readable graph (a
// workspace as one set of readers sees it) and graph version.
//
// The fast algorithms (components, SCCs, a cycle, topological order,
// PageRank, Louvain) run on request and are cached for the version.
// Betweenness and diameter on large graphs run in the background, one job
// at a time per graph; until a job finishes, requests get the newest
// finished result for that graph, with its version, so a big workspace
// never blocks a request and never recomputes per request.
type Analyzer struct {
	cfg  AnalyzerConfig
	jobs chan slowJob

	mu      sync.Mutex
	entries map[string]*analysisEntry
	order   []string // insertion order, for eviction
}

type analysisEntry struct {
	version int64
	fast    *Analysis // slow fields unset
	slow    *slowResult
	pending int64 // version of the queued or running job; 0 when none
}

type slowResult struct {
	version     int64
	betweenness map[string]float64
	diameter    int
}

type slowJob struct {
	key     string
	version int64
	g       *graph.Graph
}

// NewAnalyzer returns an analyzer. Run its background worker with Run.
func NewAnalyzer(cfg AnalyzerConfig) *Analyzer {
	if cfg.InlineLimit <= 0 {
		cfg.InlineLimit = 2000
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = 256
	}
	if cfg.Logger == nil {
		cfg.Logger = logging.Discard()
	}
	return &Analyzer{cfg: cfg, jobs: make(chan slowJob, 64), entries: map[string]*analysisEntry{}}
}

// GraphKey identifies a readable graph: the workspace and the exact set of
// notes in it. Readers who see the same notes share one cache entry.
func GraphKey(workspace string, g *graph.Graph) string {
	h := sha256.New()
	h.Write([]byte(workspace))
	for _, id := range g.Nodes() {
		h.Write([]byte{0})
		h.Write([]byte(id))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Analyze returns the analysis of g, the readable graph key at version.
func (a *Analyzer) Analyze(key string, version int64, g *graph.Graph) (Analysis, error) {
	a.mu.Lock()
	e := a.entries[key]
	if e == nil {
		e = &analysisEntry{}
		a.entries[key] = e
		a.order = append(a.order, key)
		a.evict()
	}
	var fast *Analysis
	if e.version == version {
		fast = e.fast
	}
	a.mu.Unlock()

	if fast == nil {
		computed, err := fastAnalysis(g)
		if err != nil {
			return Analysis{}, err
		}
		fast = &computed
		a.mu.Lock()
		if e.version <= version {
			e.version, e.fast = version, fast
		}
		a.mu.Unlock()
	}
	out := *fast
	out.GraphVersion = version

	a.mu.Lock()
	slow := e.slow
	if (slow == nil || slow.version != version) && g.Len() <= a.cfg.InlineLimit {
		a.mu.Unlock()
		slow = slowAnalysis(g, version)
		a.mu.Lock()
		if e.slow == nil || e.slow.version < version {
			e.slow = slow
		}
	} else if (slow == nil || slow.version != version) && e.pending != version {
		select {
		case a.jobs <- slowJob{key: key, version: version, g: g}:
			e.pending = version
		default:
			// The queue is full; the next request will try again.
		}
	}
	out.SlowPending = slow == nil || slow.version != version
	a.mu.Unlock()

	out.Betweenness = map[string]float64{}
	if slow != nil {
		out.SlowVersion, out.Diameter = slow.version, slow.diameter
		for id, b := range slow.betweenness {
			if g.Has(id) {
				out.Betweenness[id] = b
			}
		}
	}
	return out, nil
}

func (a *Analyzer) evict() {
	for len(a.order) > a.cfg.MaxEntries {
		delete(a.entries, a.order[0])
		a.order = a.order[1:]
	}
}

// Run computes queued slow metrics until ctx ends.
func (a *Analyzer) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case job := <-a.jobs:
			a.runJob(job)
		}
	}
}

// runJob computes one queued job.
func (a *Analyzer) runJob(job slowJob) {
	result := slowAnalysis(job.g, job.version)
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.entries[job.key]
	if e == nil {
		return
	}
	if e.slow == nil || e.slow.version < job.version {
		e.slow = result
	}
	if e.pending == job.version {
		e.pending = 0
	}
	a.cfg.Logger.Debug("graph metrics computed", "notes", job.g.Len(), "version", job.version)
}

// nextJob returns a queued job without blocking, for tests.
func (a *Analyzer) nextJob() (slowJob, bool) {
	select {
	case j := <-a.jobs:
		return j, true
	default:
		return slowJob{}, false
	}
}

func fastAnalysis(g *graph.Graph) (Analysis, error) {
	components := g.ConnectedComponents()
	scc := g.StronglyConnected()
	order := g.TopologicalOrder()
	ranks, err := g.PageRank(graph.DefaultPageRankOptions())
	if err != nil {
		return Analysis{}, err
	}
	communities := g.Louvain()
	return Analysis{
		ComponentOf: components.Of, Orphans: g.OrphanComponents(),
		SCCOf: scc.Of, SCCSizes: scc.Sizes(), Cycle: g.FindCycle(),
		Topological: order.Order, Unplaced: order.Unplaced,
		PageRank: ranks.Scores, CommunityOf: communities.Partition.Of, Modularity: communities.Modularity,
	}, nil
}

func slowAnalysis(g *graph.Graph, version int64) *slowResult {
	return &slowResult{version: version, betweenness: g.Betweenness(), diameter: g.Diameter().Length}
}
