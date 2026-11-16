package seed_test

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/libs/graph"
	"github.com/genuinebnt/skein-go/libs/trigram"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
	"github.com/genuinebnt/skein-go/services/search/internal/fakes"
	"github.com/genuinebnt/skein-go/services/search/internal/seed"
)

const smallDir = "../../../../testdata/seed/small"

// Quality bars for ranking on the small seed. Measured values are logged
// by the tests and reported in the pull request; the bars sit below them
// so harmless reordering does not fail CI while a real regression does.
const (
	minMeanMRR    = 0.95
	minMeanNDCG10 = 0.95
	minTypoMRR    = 0.90
)

type world struct {
	store    *fakes.Store
	index    *app.Index
	searcher *app.Searcher
	grapher  *app.Grapher
	dir      *seed.Directory
	owners   map[string]string // workspace -> owner
	stats    seed.Stats
}

var (
	loadOnce sync.Once
	loaded   *world
	loadErr  error
)

// smallWorld loads the small seed once for every test in the package.
func smallWorld(t *testing.T) *world {
	t.Helper()
	loadOnce.Do(func() { loaded, loadErr = load() })
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	return loaded
}

func load() (*world, error) {
	ctx := context.Background()
	w := &world{store: fakes.NewStore(), owners: map[string]string{}}
	cache := redisx.NewMemoryStore(time.Now)
	w.index = app.NewIndex(w.store, app.IndexConfig{})
	projector := app.NewProjector(w.store, w.index, nil, nil)
	var err error
	if w.stats, err = seed.Load(ctx, smallDir, projector, w.store); err != nil {
		return nil, err
	}
	if err := w.index.Bootstrap(ctx); err != nil {
		return nil, err
	}
	if w.dir, err = seed.NewDirectory(smallDir); err != nil {
		return nil, err
	}
	access, err := app.NewAccess(w.dir, cache)
	if err != nil {
		return nil, err
	}
	if w.searcher, err = app.NewSearcher(w.index, access, cache); err != nil {
		return nil, err
	}
	w.grapher = app.NewGrapher(w.index, access, app.NewAnalyzer(app.AnalyzerConfig{}), nil, nil, nil)
	workspaces, err := seed.ReadJSONL[struct {
		ID      string `json:"id"`
		OwnerID string `json:"owner_id"`
	}](filepath.Join(smallDir, "workspaces.jsonl"))
	if err != nil {
		return nil, err
	}
	for _, ws := range workspaces {
		w.owners[ws.ID] = ws.OwnerID
	}
	return w, nil
}

func truth[T any](t *testing.T, name string) []T {
	t.Helper()
	out, err := seed.ReadJSONL[T](filepath.Join(smallDir, "truth", name))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLoadingTwiceChangesNothing(t *testing.T) {
	w := smallWorld(t)
	if w.stats.Notes != 300 || w.stats.Applied != w.stats.Events {
		t.Fatalf("first load %+v", w.stats)
	}
	before := w.index.Position()
	again, err := seed.Load(context.Background(), smallDir, app.NewProjector(w.store, nil, nil, nil), w.store)
	if err != nil {
		t.Fatal(err)
	}
	if again.Applied != 0 {
		t.Fatalf("second load applied %d events", again.Applied)
	}
	if _, err := w.index.CatchUp(context.Background()); err != nil || w.index.Position() != before {
		t.Fatalf("position moved from %d to %d (%v)", before, w.index.Position(), err)
	}
	if err := w.index.Read(func(p *domain.Projection) error { return p.CheckInvariants() }); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryAgreesWithThePermissionMatrix(t *testing.T) {
	w := smallWorld(t)
	type row struct {
		WorkspaceID string `json:"workspace_id"`
		Subject     struct {
			UserID      *string `json:"user_id"`
			ShareLinkID *string `json:"share_link_id"`
		} `json:"subject"`
		Action     string `json:"action"`
		ResourceID string `json:"resource_id"`
		Expected   string `json:"expected"`
	}
	checked := 0
	for _, r := range truth[row](t, "permissions/matrix.jsonl") {
		if r.Action != "ACTION_NOTE_READ" || r.Subject.UserID == nil || r.Subject.ShareLinkID != nil {
			continue
		}
		notes, err := w.dir.ListReadableNotes(context.Background(), auth.Subject{UserID: *r.Subject.UserID}, r.WorkspaceID)
		if err != nil && !app.IsNotFound(err) {
			t.Fatal(err)
		}
		if got := slices.Contains(notes, r.ResourceID); got != (r.Expected == "allow") {
			t.Fatalf("user %s note %s: readable %v, matrix says %s", *r.Subject.UserID, r.ResourceID, got, r.Expected)
		}
		checked++
	}
	t.Logf("%d READ decisions agree with the matrix", checked)
}

// --- text search quality ---

type qrel struct {
	NoteID string `json:"note_id"`
	Grade  int    `json:"grade"`
}

type textQuery struct {
	ID            string `json:"id"`
	WorkspaceID   string `json:"workspace_id"`
	Query         string `json:"query"`
	Kind          string `json:"kind"`
	SubjectUserID string `json:"subject_user_id"`
	Qrels         []qrel `json:"qrels"`
}

// rankedNotes runs a text search and returns every hit's note, all pages.
func rankedNotes(t *testing.T, w *world, user, ws, query string) []string {
	t.Helper()
	var out []string
	req := app.SearchRequest{Subject: auth.Subject{UserID: user}, WorkspaceID: ws, Query: query, PageSize: app.MaxPageSize}
	for {
		page, err := w.searcher.Search(context.Background(), req)
		if err != nil {
			t.Fatalf("%q: %v", query, err)
		}
		for _, h := range page.Hits {
			out = append(out, h.Note)
		}
		if page.NextPageToken == "" {
			return out
		}
		req.PageToken = page.NextPageToken
	}
}

func reciprocalRank(ranked []string, relevant map[string]int) float64 {
	for i, n := range ranked {
		if relevant[n] > 0 {
			return 1 / float64(i+1)
		}
	}
	return 0
}

func ndcgAt(k int, ranked []string, relevant map[string]int) float64 {
	dcg := 0.0
	for i, n := range ranked[:min(k, len(ranked))] {
		dcg += (math.Pow(2, float64(relevant[n])) - 1) / math.Log2(float64(i+2))
	}
	var grades []int
	for _, g := range relevant {
		grades = append(grades, g)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(grades)))
	ideal := 0.0
	for i, g := range grades[:min(k, len(grades))] {
		ideal += (math.Pow(2, float64(g)) - 1) / math.Log2(float64(i+2))
	}
	if ideal == 0 {
		return 0
	}
	return dcg / ideal
}

func TestTextSearchMeetsTheQrels(t *testing.T) {
	w := smallWorld(t)
	var sumMRR, sumNDCG float64
	judged := 0
	var report strings.Builder
	for _, q := range truth[textQuery](t, "search/text_queries.jsonl") {
		ranked := rankedNotes(t, w, q.SubjectUserID, q.WorkspaceID, q.Query)
		relevant := map[string]int{}
		for _, r := range q.Qrels {
			relevant[r.NoteID] = r.Grade
		}
		readable, _ := w.dir.ListReadableNotes(context.Background(), auth.Subject{UserID: q.SubjectUserID}, q.WorkspaceID)
		for _, n := range ranked {
			if !slices.Contains(readable, n) {
				t.Fatalf("%s: returned unreadable note %s", q.ID, n)
			}
		}
		// Every relevant note matches every term exactly, so the search,
		// whose typo tolerance only adds matches, must find all of them.
		for _, r := range q.Qrels {
			if !slices.Contains(ranked, r.NoteID) {
				t.Errorf("%s %q: relevant note %s (grade %d) not returned", q.ID, q.Query, r.NoteID, r.Grade)
			}
		}
		if len(q.Qrels) == 0 {
			continue
		}
		mrr, ndcg := reciprocalRank(ranked, relevant), ndcgAt(10, ranked, relevant)
		sumMRR += mrr
		sumNDCG += ndcg
		judged++
		fmt.Fprintf(&report, "%-28s %-30q rel=%-3d ret=%-3d MRR=%.3f nDCG@10=%.3f\n", q.ID, q.Query, len(q.Qrels), len(ranked), mrr, ndcg)
	}
	meanMRR, meanNDCG := sumMRR/float64(judged), sumNDCG/float64(judged)
	t.Logf("per query:\n%s", report.String())
	t.Logf("text queries: %d judged, mean MRR %.4f, mean nDCG@10 %.4f", judged, meanMRR, meanNDCG)
	if meanMRR < minMeanMRR || meanNDCG < minMeanNDCG10 {
		t.Fatalf("ranking below the bar: MRR %.4f (min %.2f), nDCG@10 %.4f (min %.2f)", meanMRR, minMeanMRR, meanNDCG, minMeanNDCG10)
	}
}

func TestTypoQueriesFindTheIntendedNotes(t *testing.T) {
	w := smallWorld(t)
	type typo struct {
		ID            string   `json:"id"`
		WorkspaceID   string   `json:"workspace_id"`
		Query         string   `json:"query"`
		Intended      string   `json:"intended"`
		TargetQueryID string   `json:"target_query_id"`
		TargetNoteIDs []string `json:"target_note_ids"`
	}
	subjects := map[string]string{}
	for _, q := range truth[textQuery](t, "search/text_queries.jsonl") {
		subjects[q.ID] = q.SubjectUserID
	}
	sum, judged := 0.0, 0
	for _, q := range truth[typo](t, "search/typo_queries.jsonl") {
		if len(q.TargetNoteIDs) == 0 {
			continue
		}
		ranked := rankedNotes(t, w, subjects[q.TargetQueryID], q.WorkspaceID, q.Query)
		relevant := map[string]int{}
		for _, n := range q.TargetNoteIDs {
			relevant[n] = 1
		}
		found := 0
		for _, n := range ranked[:min(10, len(ranked))] {
			found += relevant[n]
		}
		rr := reciprocalRank(ranked, relevant)
		sum += rr
		judged++
		t.Logf("%-28s %-26q -> %-24q targets=%-3d in top 10=%-3d MRR=%.3f", q.ID, q.Query, q.Intended, len(q.TargetNoteIDs), found, rr)
		if rr == 0 {
			t.Errorf("%s: no intended note found for %q", q.ID, q.Query)
		}
	}
	mean := sum / float64(judged)
	t.Logf("typo queries: %d judged, mean MRR %.4f", judged, mean)
	if mean < minTypoMRR {
		t.Fatalf("typo MRR %.4f below %.2f", mean, minTypoMRR)
	}
}

func TestRegexSearchReturnsExactlyTheTruthMatches(t *testing.T) {
	w := smallWorld(t)
	type match struct {
		BlockID string `json:"block_id"`
		Start   int    `json:"start"`
		End     int    `json:"end"`
		NoteID  string `json:"note_id"`
	}
	type regexQuery struct {
		ID            string   `json:"id"`
		WorkspaceID   string   `json:"workspace_id"`
		Pattern       string   `json:"pattern"`
		CaseSensitive bool     `json:"case_sensitive"`
		CodeOnly      bool     `json:"code_only"`
		Languages     []string `json:"languages"`
		Matches       []match  `json:"matches"`
	}
	totalCandidates, totalMatched := 0, 0
	for _, q := range truth[regexQuery](t, "search/regex_queries.jsonl") {
		re, err := trigram.CompileRegexp(q.Pattern, q.CaseSensitive)
		if err != nil {
			t.Fatal(err)
		}
		readable, _ := w.dir.ListReadableNotes(context.Background(), auth.Subject{UserID: w.owners[q.WorkspaceID]}, q.WorkspaceID)
		var got []match
		var candidates int
		_ = w.index.Read(func(p *domain.Projection) error {
			ws, _ := p.Workspace(q.WorkspaceID)
			r := domain.Readable{}
			for _, n := range readable {
				r[n] = true
			}
			res := ws.SearchRegex(re, r, domain.Filters{CaseSensitive: q.CaseSensitive, CodeOnly: q.CodeOnly, Languages: q.Languages})
			candidates = res.Candidates
			for _, m := range res.Matches {
				for _, s := range m.Spans {
					got = append(got, match{BlockID: m.Ref.Block, Start: s.Start, End: s.End, NoteID: m.Ref.Note})
				}
			}
			return nil
		})
		// The truth lists matches in the dataset's note order, which events do
		// not carry; search orders notes by ID. Within a note both follow
		// block order, so comparing in (note, truth position) order is exact.
		want := slices.Clone(q.Matches)
		position := map[string]int{}
		for i, m := range want {
			position[fmt.Sprint(m.NoteID, m.BlockID, m.Start)] = i
		}
		byNote := func(a, b match) int {
			if c := strings.Compare(a.NoteID, b.NoteID); c != 0 {
				return c
			}
			return position[fmt.Sprint(a.NoteID, a.BlockID, a.Start)] - position[fmt.Sprint(b.NoteID, b.BlockID, b.Start)]
		}
		slices.SortStableFunc(want, byNote)
		if !slices.IsSortedFunc(got, func(a, b match) int { return strings.Compare(a.NoteID, b.NoteID) }) {
			t.Errorf("%s: hits not in note order", q.ID)
		}
		if !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("%s %q: %d matches, truth %d\n got  %v\n want %v", q.ID, q.Pattern, len(got), len(want), got, want)
		}
		totalCandidates += candidates
		totalMatched += len(q.Matches)
		// The service path must agree on the matched blocks too.
		page, err := w.searcher.Search(context.Background(), app.SearchRequest{Subject: auth.Subject{UserID: w.owners[q.WorkspaceID]},
			WorkspaceID: q.WorkspaceID, Query: q.Pattern, Mode: app.ModeRegex, CaseSensitive: q.CaseSensitive, CodeOnly: q.CodeOnly,
			Languages: q.Languages, PageSize: app.MaxPageSize})
		if err != nil {
			t.Fatal(err)
		}
		blocks := map[string]bool{}
		for _, m := range q.Matches {
			blocks[m.NoteID+"/"+m.BlockID] = true
		}
		if page.Matched != len(blocks) || len(page.Hits) != len(blocks) || page.Candidates != candidates {
			t.Errorf("%s: service matched %d blocks of %d candidates, truth %d blocks", q.ID, page.Matched, page.Candidates, len(blocks))
		}
	}
	t.Logf("regex queries: %d candidate blocks read in total for %d truth matches", totalCandidates, totalMatched)
}

func TestTitleSuggestionsCompletePrefixesAndTypos(t *testing.T) {
	w := smallWorld(t)
	type titleMatch struct {
		NoteID string `json:"note_id"`
		Title  string `json:"title"`
	}
	type prefixQuery struct {
		WorkspaceID    string       `json:"workspace_id"`
		Prefix         string       `json:"prefix"`
		Typo           bool         `json:"typo"`
		Expected       []titleMatch `json:"expected"`
		IntendedNoteID *string      `json:"intended_note_id"`
		EditDistance   int          `json:"edit_distance"`
	}
	for _, q := range truth[prefixQuery](t, "search/title_prefixes.jsonl") {
		got, err := w.searcher.SuggestTitles(context.Background(), app.TitlesRequest{Subject: auth.Subject{UserID: w.owners[q.WorkspaceID]},
			WorkspaceID: q.WorkspaceID, Prefix: q.Prefix, Limit: app.MaxTitleLimit})
		if err != nil {
			t.Fatal(err)
		}
		var exact []titleMatch
		for _, s := range got {
			if s.Distance == 0 {
				exact = append(exact, titleMatch{NoteID: s.Note, Title: s.Title})
			}
		}
		if !slices.Equal(exact, q.Expected) && !(len(exact) == 0 && len(q.Expected) == 0) {
			t.Errorf("prefix %q: exact completions %v, truth %v", q.Prefix, exact, q.Expected)
		}
		if q.Typo {
			i := slices.IndexFunc(got, func(s domain.TitleSuggestion) bool { return s.Note == *q.IntendedNoteID })
			if i < 0 || got[i].Distance > q.EditDistance {
				t.Errorf("typo prefix %q: intended note missing or too far: %v", q.Prefix, got)
			}
		}
	}
}

// --- graph truth ---

func ownerAnalysis(t *testing.T, w *world, ws string) (app.LinkGraph, app.Analysis) {
	t.Helper()
	owner := auth.Subject{UserID: w.owners[ws]}
	lg, err := w.grapher.GetLinkGraph(context.Background(), owner, ws)
	if err != nil {
		t.Fatal(err)
	}
	a, err := w.grapher.AnalyzeGraph(context.Background(), owner, ws)
	if err != nil {
		t.Fatal(err)
	}
	return lg, a
}

func groups(of map[string]int) map[string][]string {
	byLabel := map[int][]string{}
	for id, c := range of {
		byLabel[c] = append(byLabel[c], id)
	}
	out := map[string][]string{}
	for _, members := range byLabel {
		slices.Sort(members)
		out[strings.Join(members, ",")] = members
	}
	return out
}

func TestGraphMatchesSummarySCCsComponentsAndCycles(t *testing.T) {
	w := smallWorld(t)
	type summary struct {
		WorkspaceID string `json:"workspace_id"`
		Nodes       int    `json:"nodes"`
		Edges       int    `json:"edges"`
		Nontrivial  int    `json:"nontrivial_sccs"`
		Components  int    `json:"components"`
		Orphans     int    `json:"orphan_components"`
		Acyclic     bool   `json:"acyclic"`
	}
	type scc struct {
		WorkspaceID string   `json:"workspace_id"`
		Members     []string `json:"members"`
	}
	type component struct {
		WorkspaceID string   `json:"workspace_id"`
		Orphan      bool     `json:"orphan"`
		Members     []string `json:"members"`
	}
	sccs := map[string]map[string]bool{}
	for _, s := range truth[scc](t, "graph/sccs.jsonl") {
		if sccs[s.WorkspaceID] == nil {
			sccs[s.WorkspaceID] = map[string]bool{}
		}
		sccs[s.WorkspaceID][strings.Join(s.Members, ",")] = true
	}
	components := map[string]map[string]bool{}
	for _, c := range truth[component](t, "graph/components.jsonl") {
		if components[c.WorkspaceID] == nil {
			components[c.WorkspaceID] = map[string]bool{}
		}
		components[c.WorkspaceID][fmt.Sprint(c.Orphan, ":", strings.Join(c.Members, ","))] = true
	}
	// The truth counts a note linking to itself as an edge; the projection
	// drops self-links, as libs/graph requires, so they are subtracted.
	selfLinks := map[string]int{}
	links, err := seed.ReadJSONL[struct {
		WorkspaceID string  `json:"workspace_id"`
		Source      string  `json:"source_note_id"`
		Target      *string `json:"target_note_id"`
		RemovedAt   *string `json:"removed_at"`
	}](filepath.Join(smallDir, "links.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		if l.RemovedAt == nil && l.Target != nil && *l.Target == l.Source {
			selfLinks[l.WorkspaceID]++
		}
	}
	for _, s := range truth[summary](t, "graph/summary.jsonl") {
		lg, a := ownerAnalysis(t, w, s.WorkspaceID)
		if len(lg.Nodes) != s.Nodes || len(lg.Edges) != s.Edges-selfLinks[s.WorkspaceID] {
			t.Errorf("%s: %d nodes %d edges, truth %d and %d with %d self-links", s.WorkspaceID, len(lg.Nodes), len(lg.Edges), s.Nodes, s.Edges, selfLinks[s.WorkspaceID])
		}
		nontrivial := map[string]bool{}
		for key, members := range groups(a.SCCOf) {
			if len(members) > 1 {
				nontrivial[key] = true
			}
		}
		if len(nontrivial) != s.Nontrivial || !mapsEqual(nontrivial, sccs[s.WorkspaceID]) {
			t.Errorf("%s: %d non-trivial SCCs differ from the truth's %d", s.WorkspaceID, len(nontrivial), s.Nontrivial)
		}
		orphan := map[int]bool{}
		for _, c := range a.Orphans {
			orphan[c] = true
		}
		got := map[string]bool{}
		for _, members := range groups(a.ComponentOf) {
			got[fmt.Sprint(orphan[a.ComponentOf[members[0]]], ":", strings.Join(members, ","))] = true
		}
		if len(got) != s.Components || len(a.Orphans) != s.Orphans || !mapsEqual(got, components[s.WorkspaceID]) {
			t.Errorf("%s: components or orphans differ from the truth", s.WorkspaceID)
		}
		if s.Acyclic != (len(a.Cycle) == 0) {
			t.Errorf("%s: cycle %v, truth acyclic=%v", s.WorkspaceID, a.Cycle, s.Acyclic)
		}
		edges := map[string]bool{}
		for _, e := range lg.Edges {
			edges[e.From+">"+e.To] = true
		}
		for i := 1; i < len(a.Cycle); i++ {
			if !edges[a.Cycle[i-1]+">"+a.Cycle[i]] {
				t.Errorf("%s: cycle step %s -> %s is not a link", s.WorkspaceID, a.Cycle[i-1], a.Cycle[i])
			}
		}
		if len(a.Cycle) > 0 && a.Cycle[0] != a.Cycle[len(a.Cycle)-1] {
			t.Errorf("%s: cycle does not close", s.WorkspaceID)
		}
		if a.SlowPending || len(a.Betweenness) != s.Nodes {
			t.Errorf("%s: slow metrics missing on a small graph", s.WorkspaceID)
		}
	}
}

func mapsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func TestBlastRadiusMatchesTheTruthInBothDirections(t *testing.T) {
	w := smallWorld(t)
	type radius struct {
		WorkspaceID string         `json:"workspace_id"`
		NoteID      string         `json:"note_id"`
		Direction   string         `json:"direction"`
		MaxHops     int            `json:"max_hops"`
		Reached     int            `json:"reached"`
		RingSizes   []int          `json:"ring_sizes"`
		Hops        map[string]int `json:"hops"`
	}
	directions := map[string]graph.Direction{"outbound": graph.Cites, "inbound": graph.CitedBy}
	seen := map[string]int{}
	for _, r := range truth[radius](t, "graph/blast_radius.jsonl") {
		dir, ok := directions[r.Direction]
		if !ok {
			t.Fatalf("unknown direction %q", r.Direction)
		}
		seen[r.Direction]++
		rings, err := w.grapher.BlastRadius(context.Background(), auth.Subject{UserID: w.owners[r.WorkspaceID]}, r.NoteID, r.MaxHops, dir)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(rings.Sizes, r.RingSizes) || len(rings.Hops) != r.Reached {
			t.Errorf("%s %s: rings %v, truth %v", r.NoteID, r.Direction, rings.Sizes, r.RingSizes)
		}
		for note, hop := range r.Hops {
			if rings.Hops[note] != hop {
				t.Errorf("%s %s: %s at hop %d, truth %d", r.NoteID, r.Direction, note, rings.Hops[note], hop)
			}
		}
	}
	if seen["outbound"] == 0 || seen["inbound"] == 0 {
		t.Fatalf("truth covers directions %v", seen)
	}
}

func TestShortestPathsMatchTheTruth(t *testing.T) {
	w := smallWorld(t)
	type path struct {
		WorkspaceID string   `json:"workspace_id"`
		From        string   `json:"from"`
		To          string   `json:"to"`
		Found       bool     `json:"found"`
		Distance    *int     `json:"distance"`
		Path        []string `json:"path"`
		Unique      bool     `json:"unique"`
	}
	notFound := 0
	for _, q := range truth[path](t, "graph/shortest_paths.jsonl") {
		got, err := w.grapher.ShortestPath(context.Background(), auth.Subject{UserID: w.owners[q.WorkspaceID]}, q.From, q.To, false)
		if err != nil {
			t.Fatal(err)
		}
		if got.Found != q.Found {
			t.Errorf("%s -> %s: found %v, truth %v", q.From, q.To, got.Found, q.Found)
			continue
		}
		if !q.Found {
			notFound++
			continue
		}
		if got.Hops != *q.Distance || got.Nodes[0] != q.From || got.Nodes[len(got.Nodes)-1] != q.To {
			t.Errorf("%s -> %s: %d hops, truth %d", q.From, q.To, got.Hops, *q.Distance)
		}
		if q.Unique && !slices.Equal(got.Nodes, q.Path) {
			t.Errorf("%s -> %s: unique path %v, truth %v", q.From, q.To, got.Nodes, q.Path)
		}
	}
	if notFound == 0 {
		t.Fatal("truth has no unreachable pair; the test would miss found=false")
	}
}
