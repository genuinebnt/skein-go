package app_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/libs/graph"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/events"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
	"github.com/genuinebnt/skein-go/services/search/internal/fakes"
)

func testSeed(t testing.TB) uint64 {
	t.Helper()
	seed := rand.Uint64()
	if v := os.Getenv("SKEIN_SEED"); v != "" {
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("SKEIN_SEED=%q: %v", v, err)
		}
		seed = parsed
	}
	t.Logf("seed %d (replay with SKEIN_SEED=%d)", seed, seed)
	return seed
}

// harness wires the use cases to fakes, as main wires them to adapters.
type harness struct {
	t         *testing.T
	store     *fakes.Store
	dir       *fakes.Directory
	cache     *redisx.MemoryStore
	index     *app.Index
	projector *app.Projector
	searcher  *app.Searcher
	grapher   *app.Grapher
	analyzer  *app.Analyzer
	relevance *fakes.Relevance
	nextID    int
}

func newHarness(t *testing.T, analyzer app.AnalyzerConfig) *harness {
	t.Helper()
	h := &harness{t: t, store: fakes.NewStore(), dir: fakes.NewDirectory(), cache: redisx.NewMemoryStore(time.Now),
		relevance: &fakes.Relevance{Scores: map[string]map[string]float64{}}}
	inv, err := redisx.NewInvalidator(h.cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.index = app.NewIndex(h.store, app.IndexConfig{OnApplied: func(ctx context.Context, notes []string) {
		for _, n := range notes {
			_, _ = inv.InvalidateTags(ctx, "note:"+n)
		}
	}})
	h.projector = app.NewProjector(h.store, h.index, inv, nil)
	access, err := app.NewAccess(h.dir, h.cache)
	if err != nil {
		t.Fatal(err)
	}
	if h.searcher, err = app.NewSearcher(h.index, access, h.cache); err != nil {
		t.Fatal(err)
	}
	h.analyzer = app.NewAnalyzer(analyzer)
	h.grapher = app.NewGrapher(h.index, access, h.analyzer, nil, h.relevance, nil)
	return h
}

func (h *harness) message(e *skeinv1.Event) events.Message {
	h.nextID++
	data, err := proto.Marshal(e)
	if err != nil {
		h.t.Fatal(err)
	}
	return events.Message{Envelope: events.Envelope{ID: fmt.Sprintf("ev-%06d", h.nextID), Source: "documents", Payload: data}}
}

func (h *harness) deliver(msg events.Message) app.Outcome {
	h.t.Helper()
	out, err := h.projector.HandleDocuments(context.Background(), msg)
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) send(e *skeinv1.Event) { h.deliver(h.message(e)) }

func (h *harness) catchUp() {
	h.t.Helper()
	if _, err := h.index.CatchUp(context.Background()); err != nil {
		h.t.Fatal(err)
	}
	if err := h.index.Read(func(p *domain.Projection) error { return p.CheckInvariants() }); err != nil {
		h.t.Fatal(err)
	}
}

func created(note, ws, title string) *skeinv1.Event {
	return &skeinv1.Event{Payload: &skeinv1.Event_NoteCreated{NoteCreated: &skeinv1.NoteCreated{NoteId: note, WorkspaceId: ws, Title: title}}}
}

func blocks(note string, texts ...string) *skeinv1.Event {
	bc := &skeinv1.BlocksChanged{NoteId: note}
	for i, text := range texts {
		bc.Blocks = append(bc.Blocks, &skeinv1.ChangedBlock{BlockId: fmt.Sprint("b", i), Kind: "paragraph", Text: text})
	}
	return &skeinv1.Event{Payload: &skeinv1.Event_BlocksChanged{BlocksChanged: bc}}
}

func links(note string, targets ...string) *skeinv1.Event {
	return &skeinv1.Event{Payload: &skeinv1.Event_LinksChanged{LinksChanged: &skeinv1.LinksChanged{NoteId: note, Targets: targets}}}
}

func purged(note string) *skeinv1.Event {
	return &skeinv1.Event{Payload: &skeinv1.Event_NotePurged{NotePurged: &skeinv1.NotePurged{NoteId: note}}}
}

var alice = auth.Subject{UserID: "alice"}

func (h *harness) search(req app.SearchRequest) app.SearchPage {
	h.t.Helper()
	if req.Subject.UserID == "" {
		req.Subject = alice
	}
	page, err := h.searcher.Search(context.Background(), req)
	if err != nil {
		h.t.Fatal(err)
	}
	return page
}

func hitNotes(p app.SearchPage) []string {
	var out []string
	for _, h := range p.Hits {
		out = append(out, h.Note)
	}
	return out
}

func TestDuplicateDeliveryIsAppliedOnce(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{})
	msg := h.message(created("n1", "w", "Raft"))
	if h.deliver(msg) != app.OutcomeApplied || h.deliver(msg) != app.OutcomeDuplicate {
		t.Fatal("second delivery was not a duplicate")
	}
	h.catchUp()
	if h.index.Position() != 1 || h.store.LastEvent(app.DocumentsConsumer) != msg.ID {
		t.Fatalf("position %d, last event %q", h.index.Position(), h.store.LastEvent(app.DocumentsConsumer))
	}
}

func TestUndecodablePayloadIsUnprocessable(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{})
	_, err := h.projector.HandleDocuments(context.Background(), events.Message{Envelope: events.Envelope{ID: "x", Payload: []byte{0xff, 0xff}}})
	if !errors.Is(err, app.ErrUnprocessable) {
		t.Fatalf("error %v", err)
	}
}

// TestRandomStreamsWithDuplicatesAndFailuresConverge delivers a random
// event stream with redeliveries and failed transactions (which the
// subscriber redelivers), and checks that the projection equals one built
// by applying each event once.
func TestRandomStreamsWithDuplicatesAndFailuresConverge(t *testing.T) {
	r := rand.New(rand.NewPCG(testSeed(t), 7))
	for range 20 {
		h := newHarness(t, app.AnalyzerConfig{})
		want := domain.NewProjection()
		states := map[string]*domain.NoteState{}
		var sent []events.Message
		for i := range 150 {
			note := fmt.Sprint("n", r.IntN(5))
			var e *skeinv1.Event
			switch r.IntN(5) {
			case 0:
				e = created(note, "w", fmt.Sprint("title ", r.IntN(3)))
			case 1:
				e = links(note, fmt.Sprint("n", r.IntN(5)), fmt.Sprint("n", r.IntN(5)))
			case 2:
				if r.IntN(6) == 0 {
					e = purged(note)
					break
				}
				fallthrough
			default:
				e = blocks(note, fmt.Sprint("raft log ", r.IntN(9)))
			}
			msg := h.message(e)
			if r.IntN(5) == 0 {
				h.store.FailNextApply = errors.New("connection reset")
				if _, err := h.projector.HandleDocuments(context.Background(), msg); err == nil {
					t.Fatal("failed transaction reported success")
				}
			}
			h.deliver(msg)
			m := app.MutationOf(e)
			next, _ := domain.ApplyMutation(states[note], *m)
			states[note] = next
			want.Apply(note, next, int64(i+1))
			sent = append(sent, msg)
			if r.IntN(4) == 0 {
				h.deliver(sent[r.IntN(len(sent))])
			}
			if r.IntN(10) == 0 {
				h.catchUp()
			}
		}
		h.catchUp()
		_ = h.index.Read(func(p *domain.Projection) error {
			if !domain.EqualContent(p, want) {
				t.Fatal("projection differs from applying each event once")
			}
			return nil
		})
	}
}

func TestRestartFromSnapshotReplaysOnlyLaterChangesAndMatchesAFullLoad(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{})
	h.send(created("a", "w", "Raft log compaction"))
	h.send(blocks("a", "snapshot the log, then truncate"))
	h.catchUp()
	if err := h.index.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.send(created("b", "w", "Raft snapshots"))
	h.send(links("a", "b"))

	restarted := app.NewIndex(h.store, app.IndexConfig{})
	if err := restarted.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	fromTables := app.NewIndex(h.store, app.IndexConfig{})
	// The feed is pruned past the snapshot's position: the follower meets the
	// gap and must reload every note from the tables.
	h.store.Prune(3)
	if err := fromTables.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.catchUp()
	for _, ix := range []*app.Index{restarted, fromTables} {
		if ix.Position() != 4 {
			t.Fatalf("position %d after restart", ix.Position())
		}
		_ = ix.Read(func(p *domain.Projection) error {
			return h.index.Read(func(live *domain.Projection) error {
				if err := p.CheckInvariants(); err != nil {
					t.Fatal(err)
				}
				if !domain.EqualContent(p, live) {
					t.Fatal("restarted projection differs")
				}
				return nil
			})
		})
	}
}

func TestPurgeErasesTheNoteEverywhere(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{})
	h.dir.Grant("alice", "w", "secret", "other")
	h.send(created("secret", "w", "Merger plan"))
	h.send(blocks("secret", "codename quilfen is confidential"))
	h.send(created("other", "w", "Lunch"))
	h.catchUp()
	if err := h.index.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := hitNotes(h.search(app.SearchRequest{WorkspaceID: "w", Query: "quilfen"})); !slices.Equal(got, []string{"secret"}) {
		t.Fatalf("before purge %v", got)
	}
	h.send(purged("secret"))
	if h.store.SnapshotCount() != 0 {
		t.Fatal("snapshots holding the purged text survived the purge")
	}
	h.send(blocks("secret", "codename quilfen is confidential")) // a late duplicate edit
	h.catchUp()
	if got := h.search(app.SearchRequest{WorkspaceID: "w", Query: "quilfen"}); len(got.Hits) != 0 || got.Candidates != 0 {
		t.Fatalf("purged note still found: %+v", got)
	}
	_ = h.index.Read(func(p *domain.Projection) error {
		if _, ok := p.WorkspaceOf("secret"); ok {
			t.Fatal("purged note still in the projection")
		}
		return nil
	})
}

func TestSearchNeverReturnsOrCountsUnreadableNotes(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{})
	for i := range 6 {
		id := fmt.Sprint("n", i)
		h.send(created(id, "w", fmt.Sprint("note ", i)))
		h.send(blocks(id, "raft compaction details", "func Close() error"))
	}
	h.catchUp()
	h.dir.Grant("alice", "w", "n1", "n3")
	page := h.search(app.SearchRequest{WorkspaceID: "w", Query: "compaction"})
	if !slices.Equal(hitNotes(page), []string{"n1", "n3"}) || page.Candidates != 2 || page.Matched != 2 {
		t.Fatalf("text %+v", page)
	}
	page = h.search(app.SearchRequest{WorkspaceID: "w", Query: `Close\(\)`, Mode: app.ModeRegex, CaseSensitive: true})
	if !slices.Equal(hitNotes(page), []string{"n1", "n3"}) || page.Candidates != 2 {
		t.Fatalf("regex %+v", page)
	}
	if _, err := h.searcher.Search(context.Background(), app.SearchRequest{Subject: auth.Subject{UserID: "mallory"}, WorkspaceID: "w", Query: "raft"}); !app.IsNotFound(err) {
		t.Fatalf("non-member got %v", err)
	}
}

func TestAccessChangedDropsCachedPermissions(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{})
	h.send(created("n1", "w", "Raft"))
	h.send(created("n2", "w", "Raft too"))
	h.catchUp()
	h.dir.Grant("alice", "w", "n1", "n2")
	if got := hitNotes(h.search(app.SearchRequest{WorkspaceID: "w", Query: "raft"})); len(got) != 2 {
		t.Fatalf("before revocation %v", got)
	}
	h.dir.Grant("alice", "w", "n1")
	ev := h.message(&skeinv1.Event{Payload: &skeinv1.Event_AccessChanged{AccessChanged: &skeinv1.AccessChanged{WorkspaceId: "w", NoteId: "n2", UserId: "alice", Revoked: true}}})
	if out, err := h.projector.HandleIdentity(context.Background(), ev); err != nil || out != app.OutcomeApplied {
		t.Fatalf("identity event: %v %v", out, err)
	}
	if got := hitNotes(h.search(app.SearchRequest{WorkspaceID: "w", Query: "raft"})); !slices.Equal(got, []string{"n1"}) {
		t.Fatalf("after revocation %v: the cached permission survived the event", got)
	}
}

func TestIdentityUnreachableFailsClosed(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{})
	h.dir.Err = fmt.Errorf("%w: identity down", app.ErrUnavailable)
	if _, err := h.searcher.Search(context.Background(), app.SearchRequest{Subject: alice, WorkspaceID: "w", Query: "x"}); !errors.Is(err, app.ErrUnavailable) {
		t.Fatalf("error %v", err)
	}
}

func TestPagesAreCursorBasedAndTokensAreBoundToTheirRequest(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{})
	var ids []string
	for i := range 7 {
		id := fmt.Sprint("n", i)
		ids = append(ids, id)
		h.send(created(id, "w", "page"))
		h.send(blocks(id, "INC-100"+fmt.Sprint(i)))
	}
	h.catchUp()
	h.dir.Grant("alice", "w", ids...)
	var all []string
	req := app.SearchRequest{WorkspaceID: "w", Query: `INC-\d+`, Mode: app.ModeRegex, PageSize: 3}
	for {
		page := h.search(req)
		all = append(all, hitNotes(page)...)
		if page.NextPageToken == "" {
			break
		}
		req.PageToken = page.NextPageToken
	}
	if !slices.Equal(all, ids) {
		t.Fatalf("paged %v", all)
	}
	first := h.search(app.SearchRequest{WorkspaceID: "w", Query: "page", PageSize: 2})
	_, err := h.searcher.Search(context.Background(), app.SearchRequest{Subject: alice, WorkspaceID: "w", Query: "other", PageToken: first.NextPageToken})
	var fe *app.FieldError
	if !errors.As(err, &fe) || fe.Field != "page.page_token" {
		t.Fatalf("token replayed against another query: %v", err)
	}
	_, err = h.searcher.Search(context.Background(), app.SearchRequest{Subject: alice, WorkspaceID: "w", Query: "(", Mode: app.ModeRegex})
	if !errors.As(err, &fe) || fe.Field != "query" {
		t.Fatalf("bad regex: %v", err)
	}
}

func TestEditedNoteIsNotServedFromAStaleCachedPage(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{})
	h.dir.Grant("alice", "w", "n1")
	h.send(created("n1", "w", "Notes"))
	h.send(blocks("n1", "zebra"))
	h.catchUp()
	if len(h.search(app.SearchRequest{WorkspaceID: "w", Query: "zebra"}).Hits) != 1 {
		t.Fatal("not found before edit")
	}
	h.send(blocks("n1", "giraffe"))
	h.catchUp()
	if got := h.search(app.SearchRequest{WorkspaceID: "w", Query: "zebra"}); len(got.Hits) != 0 {
		t.Fatalf("stale page served: %+v", got)
	}
}

func TestBlastRadiusOfAnUnreadableNoteIsNotFound(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{})
	h.send(created("a", "w", "A"))
	h.send(created("b", "w", "B"))
	h.send(created("c", "w", "C"))
	h.send(links("a", "c"))
	h.send(links("b", "c"))
	h.catchUp()
	h.dir.Grant("alice", "w", "a", "c")
	rings, err := h.grapher.BlastRadius(context.Background(), alice, "c", 0, graph.CitedBy)
	if err != nil || len(rings.Hops) != 2 || !slices.Equal(rings.Sizes, []int{1, 1}) {
		t.Fatalf("rings %+v %v: b is unreadable and must not count", rings, err)
	}
	if _, err := h.grapher.BlastRadius(context.Background(), alice, "b", 0, graph.CitedBy); !app.IsNotFound(err) {
		t.Fatalf("unreadable note: %v", err)
	}
	if _, err := h.grapher.ShortestPath(context.Background(), alice, "a", "b", false); !app.IsNotFound(err) {
		t.Fatalf("path to unreadable note: %v", err)
	}
}

func TestSlowMetricsOfLargeGraphsComeFromTheBackgroundWithTheirVersion(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{InlineLimit: 2})
	h.send(created("a", "w", "A"))
	h.send(created("b", "w", "B"))
	h.send(created("c", "w", "C"))
	h.send(links("a", "b"))
	h.send(links("b", "c"))
	h.catchUp()
	h.dir.Grant("alice", "w", "a", "b", "c")
	first, err := h.grapher.AnalyzeGraph(context.Background(), alice, "w")
	if err != nil {
		t.Fatal(err)
	}
	if !first.SlowPending || first.SlowVersion != 0 || first.GraphVersion != 5 || len(first.Topological) != 3 {
		t.Fatalf("first call %+v", first)
	}
	app.RunPendingJobs(h.analyzer)
	second, _ := h.grapher.AnalyzeGraph(context.Background(), alice, "w")
	if second.SlowPending || second.SlowVersion != 5 || second.Diameter != 2 || second.Betweenness["b"] != 1 {
		t.Fatalf("after the job %+v", second)
	}
	h.send(links("c", "a"))
	h.catchUp()
	third, _ := h.grapher.AnalyzeGraph(context.Background(), alice, "w")
	if !third.SlowPending || third.SlowVersion != 5 || third.GraphVersion != 6 || len(third.Cycle) == 0 {
		t.Fatalf("after a change %+v: stale metrics must be labelled with their version", third)
	}
}

func TestReadingPlanIncludesPrerequisitesFirst(t *testing.T) {
	h := newHarness(t, app.AnalyzerConfig{})
	h.send(created("overview", "w", "Consensus overview"))
	h.send(blocks("overview", "words "))
	h.send(created("raft", "w", "Raft snapshots"))
	h.send(links("raft", "overview"))
	h.send(created("other", "w", "Gardening"))
	h.catchUp()
	h.dir.Grant("alice", "w", "overview", "raft", "other")
	h.relevance.Scores["consensus protocols"] = map[string]float64{"raft": 0.9, "overview": 0.2}
	plan, err := h.grapher.ReadingPlan(context.Background(), alice, "w", "consensus protocols", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Notes) != 2 || plan.Notes[0].Note != "overview" || plan.Notes[1].Note != "raft" || plan.TotalValue != 1.1 {
		t.Fatalf("plan %+v", plan)
	}
	if plan.Notes[1].Reason != "most relevant to the topic; needs Consensus overview first" {
		t.Fatalf("reason %q", plan.Notes[1].Reason)
	}
	if _, err := h.grapher.ReadingPlan(context.Background(), alice, "w", "x", 0); err == nil {
		t.Fatal("zero minutes accepted")
	}
}
