//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/libs/crdt"
)

// Journeys over the seed dataset (testdata/seed/small, loaded by make
// up-native), checked against its ground truth (docs/seed-data.md). They
// only read, so they can run any number of times. Seed users have no
// passwords: they sign in through identity's dev-only session route.
//
// The stack runs on the real clock, not the dataset's reference time
// (2026-11-01). Every check here is one whose answer cannot change after
// that time: share-link rows (which expire) are left out.

// readJSONL decodes every line of a seed file.
func readJSONL[T any](t *testing.T, rel string) []T {
	t.Helper()
	f, err := os.Open(filepath.Join(seedDir, rel))
	if err != nil {
		t.Fatalf("seed dataset: %v (set E2E_SEED_DIR)", err)
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		out = append(out, v)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

type seedMembership struct {
	WorkspaceID string  `json:"workspace_id"`
	UserID      string  `json:"user_id"`
	Role        string  `json:"role"`
	RemovedAt   *string `json:"removed_at"`
}

type seedNote struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	Trashed     bool   `json:"trashed"`
}

// seedSessions signs in seed users once per test binary run and shares the
// sessions (every journey reads only).
var seedSessions = struct {
	sync.Mutex
	users map[string]*user
}{users: map[string]*user{}}

func asSeedUser(t *testing.T, id string) *user {
	t.Helper()
	seedSessions.Lock()
	defer seedSessions.Unlock()
	if u, ok := seedSessions.users[id]; ok {
		u.t = t
		return u
	}
	u := seedUser(t, id)
	seedSessions.users[id] = u
	return u
}

// workspaceOwners maps each seed workspace to its owner.
func workspaceOwners(t *testing.T) map[string]string {
	owners := map[string]string{}
	for _, m := range readJSONL[seedMembership](t, "memberships.jsonl") {
		if m.Role == "owner" && m.RemovedAt == nil {
			owners[m.WorkspaceID] = m.UserID
		}
	}
	return owners
}

func every[T any](items []T, step int) []T {
	var out []T
	for i := 0; i < len(items); i += step {
		out = append(out, items[i])
	}
	return out
}

// A replica syncing a seed note from nothing ends with exactly the text and
// version vector the dataset's CRDT truth gives: the op log documents
// replayed from the seed is the one the generator produced.
func TestSeedNotesSyncToTheirTruthTextAndVersion(t *testing.T) {
	t.Parallel()
	type final struct {
		NoteID        string `json:"note_id"`
		WorkspaceID   string `json:"workspace_id"`
		Text          string `json:"text"`
		VersionVector []struct {
			ReplicaID string `json:"replica_id"`
			Counter   uint64 `json:"counter"`
		} `json:"version_vector"`
	}
	trashed := map[string]bool{}
	for _, n := range readJSONL[seedNote](t, "notes.jsonl") {
		trashed[n.ID] = n.Trashed
	}
	owners := workspaceOwners(t)
	checked := 0
	for _, f := range every(readJSONL[final](t, "truth/crdt/final.jsonl"), 25) {
		if trashed[f.NoteID] {
			continue
		}
		want := crdt.VersionVector{}
		for _, e := range f.VersionVector {
			r, err := strconv.ParseUint(e.ReplicaID, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			want[crdt.ReplicaID(r)] = e.Counter
		}
		r := newReplica(t, asSeedUser(t, owners[f.WorkspaceID]), f.NoteID).mustConnect()
		eventually(t, longWait, "note "+f.NoteID+" catches up", func() (bool, string) {
			return r.version().Equal(want), fmt.Sprintf("%d of %d ops", r.version().Total(), want.Total())
		})
		if got := r.text(); got != f.Text {
			t.Errorf("note %s: synced text differs from the truth (%d vs %d code points)", f.NoteID, len([]rune(got)), len([]rune(f.Text)))
		}
		r.disconnect()
		checked++
	}
	if checked < 5 {
		t.Fatalf("checked only %d notes", checked)
	}
}

// A sample of the permission matrix, asked the way a user would: reading a
// note through the gateway is 200 when the truth allows it and 404 (never
// 403, so IDs cannot be probed) when it denies it.
func TestSeedPermissionMatrixHoldsThroughTheGateway(t *testing.T) {
	t.Parallel()
	type row struct {
		Subject struct {
			UserID      *string `json:"user_id"`
			AgentRunID  *string `json:"agent_run_id"`
			ShareLinkID *string `json:"share_link_id"`
		} `json:"subject"`
		Action     string `json:"action"`
		ResourceID string `json:"resource_id"`
		Expected   string `json:"expected"`
		Rule       string `json:"rule"`
	}
	var reads []row
	for _, r := range readJSONL[row](t, "truth/permissions/matrix.jsonl") {
		if r.Action == "ACTION_NOTE_READ" && r.Subject.UserID != nil && r.Subject.AgentRunID == nil && r.Subject.ShareLinkID == nil {
			reads = append(reads, r)
		}
	}
	sample := every(reads, 9)
	rules := map[string]bool{}
	for _, r := range sample {
		u := asSeedUser(t, *r.Subject.UserID)
		got := u.do(http.MethodGet, "/v1/notes/"+r.ResourceID, nil)
		want := http.StatusOK
		if r.Expected == "deny" {
			want = http.StatusNotFound
		}
		if got.status != want {
			t.Errorf("user %s reading %s (%s): HTTP %d, want %d", *r.Subject.UserID, r.ResourceID, r.Rule, got.status, want)
		}
		rules[r.Rule] = true
	}
	for _, rule := range []string{"workspace_role:member", "not_member", "membership_removed", "guest_without_grant", "note_override:viewer"} {
		if !rules[rule] {
			t.Errorf("the sample never exercised rule %s", rule)
		}
	}
	t.Logf("checked %d of %d read rows across %d rules", len(sample), len(reads), len(rules))
}

// Text search finds every relevant note of each anchor query (the qrels
// are exhaustive), for the query's subject.
func TestSeedSearchFindsEveryRelevantNote(t *testing.T) {
	t.Parallel()
	type query struct {
		ID            string `json:"id"`
		WorkspaceID   string `json:"workspace_id"`
		Query         string `json:"query"`
		Kind          string `json:"kind"`
		SubjectUserID string `json:"subject_user_id"`
		Qrels         []struct {
			NoteID string `json:"note_id"`
		} `json:"qrels"`
	}
	for _, q := range readJSONL[query](t, "truth/search/text_queries.jsonl") {
		if q.Kind != "anchor" && q.Kind != "permission" && q.Kind != "codename" {
			continue
		}
		u := asSeedUser(t, q.SubjectUserID)
		found := map[string]bool{}
		params := url.Values{"query": {q.Query}, "mode": {"SEARCH_MODE_TEXT"}, "page.pageSize": {"500"}}
		for page := 0; page < 20; page++ {
			var resp skeinv1.SearchResponse
			decode(t, "search "+q.ID, u.do(http.MethodGet, "/v1/workspaces/"+q.WorkspaceID+"/search?"+params.Encode(), nil), &resp)
			for _, h := range resp.GetHits() {
				found[h.GetLocation().GetNoteId()] = true
			}
			if resp.GetPage().GetNextPageToken() == "" {
				break
			}
			params.Set("page.pageToken", resp.GetPage().GetNextPageToken())
		}
		var missing []string
		relevant := map[string]bool{}
		for _, rel := range q.Qrels {
			relevant[rel.NoteID] = true
			if !found[rel.NoteID] {
				missing = append(missing, rel.NoteID)
			}
		}
		if len(missing) > 0 {
			t.Errorf("%s %q (%s): %d of %d relevant notes missing: %v", q.ID, q.Query, q.Kind, len(missing), len(q.Qrels), missing)
		}
		if q.Kind == "permission" {
			for id := range found {
				if !relevant[id] {
					t.Errorf("%s %q: the subject got note %s, which the qrels (their readable notes) exclude", q.ID, q.Query, id)
				}
			}
		}
	}
}

// Blast radius and shortest paths over the seed graph equal the truth.
func TestSeedGraphMatchesTheTruth(t *testing.T) {
	t.Parallel()
	owners := workspaceOwners(t)
	type blast struct {
		WorkspaceID string            `json:"workspace_id"`
		NoteID      string            `json:"note_id"`
		Direction   string            `json:"direction"`
		MaxHops     uint32            `json:"max_hops"`
		Hops        map[string]uint32 `json:"hops"`
	}
	for _, b := range readJSONL[blast](t, "truth/graph/blast_radius.jsonl") {
		if b.Hops == nil {
			continue // more than 2,000 reached; only counts are recorded
		}
		direction := "BLAST_DIRECTION_CITES"
		if b.Direction == "inbound" {
			direction = "BLAST_DIRECTION_CITED_BY"
		}
		var resp skeinv1.BlastRadiusResponse
		asSeedUser(t, owners[b.WorkspaceID]).call(http.MethodGet,
			fmt.Sprintf("/v1/notes/%s/blast-radius?direction=%s&maxHops=%d", b.NoteID, direction, b.MaxHops), nil, &resp)
		if len(resp.GetHops()) != len(b.Hops) {
			t.Errorf("blast radius of %s %s: %d notes, want %d", b.NoteID, b.Direction, len(resp.GetHops()), len(b.Hops))
			continue
		}
		for id, hop := range b.Hops {
			if got, ok := resp.GetHops()[id]; !ok || got != hop {
				t.Errorf("blast radius of %s %s: %s at hop %d (present %v), want %d", b.NoteID, b.Direction, id, got, ok, hop)
			}
		}
	}

	type path struct {
		WorkspaceID string   `json:"workspace_id"`
		From        string   `json:"from"`
		To          string   `json:"to"`
		Found       bool     `json:"found"`
		Distance    int      `json:"distance"`
		Path        []string `json:"path"`
		Unique      bool     `json:"unique"`
	}
	for _, p := range readJSONL[path](t, "truth/graph/shortest_paths.jsonl") {
		var resp skeinv1.ShortestPathResponse
		asSeedUser(t, owners[p.WorkspaceID]).call(http.MethodGet,
			"/v1/notes/"+p.From+"/shortest-path/"+p.To+"?metric=PATH_METRIC_HOPS", nil, &resp)
		if resp.GetFound() != p.Found {
			t.Errorf("path %s -> %s: found %v, want %v", p.From, p.To, resp.GetFound(), p.Found)
			continue
		}
		if !p.Found {
			continue
		}
		if len(resp.GetPath())-1 != p.Distance {
			t.Errorf("path %s -> %s: %d hops, want %d", p.From, p.To, len(resp.GetPath())-1, p.Distance)
		}
		if p.Unique && !slices.Equal(resp.GetPath(), p.Path) {
			t.Errorf("path %s -> %s: %v, want the unique %v", p.From, p.To, resp.GetPath(), p.Path)
		}
	}
}

// Title completion returns the expected titles for exact prefixes, and the
// intended note for misspelt ones.
func TestSeedTitleSuggestionsMatchTheTruth(t *testing.T) {
	t.Parallel()
	owners := workspaceOwners(t)
	type prefix struct {
		WorkspaceID string `json:"workspace_id"`
		Prefix      string `json:"prefix"`
		Typo        bool   `json:"typo"`
		Expected    []struct {
			NoteID string `json:"note_id"`
		} `json:"expected"`
		IntendedNoteID *string `json:"intended_note_id"`
	}
	for _, p := range readJSONL[prefix](t, "truth/search/title_prefixes.jsonl") {
		var resp skeinv1.SuggestTitlesResponse
		r := asSeedUser(t, owners[p.WorkspaceID]).do(http.MethodGet, "/v1/workspaces/"+p.WorkspaceID+"/title-suggestions?"+
			url.Values{"prefix": {p.Prefix}, "limit": {"50"}}.Encode(), nil)
		decode(t, "suggest "+p.Prefix, r, &resp)
		got := map[string]bool{}
		for _, s := range resp.GetSuggestions() {
			got[s.GetNoteId()] = true
		}
		if p.Typo && p.IntendedNoteID != nil {
			if !got[*p.IntendedNoteID] {
				t.Errorf("misspelt prefix %q: the intended note is not suggested (%d suggestions)", p.Prefix, len(resp.GetSuggestions()))
			}
			continue
		}
		for i, e := range p.Expected {
			if i >= 50 {
				break
			}
			if !got[e.NoteID] {
				t.Errorf("prefix %q: %s not suggested", p.Prefix, e.NoteID)
			}
		}
	}
}

// A removed member keeps a valid session but sees nothing of the workspace.
func TestSeedRemovedMembersSeeNothing(t *testing.T) {
	t.Parallel()
	removed := 0
	for _, m := range readJSONL[seedMembership](t, "memberships.jsonl") {
		if m.RemovedAt == nil {
			continue
		}
		u := asSeedUser(t, m.UserID)
		r := u.do(http.MethodGet, "/v1/workspaces/"+m.WorkspaceID+"/notes", nil)
		if r.status == http.StatusOK {
			var list skeinv1.ListNotesResponse
			decode(t, "list notes", r, &list)
			if len(list.GetNotes()) > 0 {
				t.Errorf("removed member %s lists %d notes of %s", m.UserID, len(list.GetNotes()), m.WorkspaceID)
			}
		} else if r.status != http.StatusNotFound && r.status != http.StatusForbidden {
			t.Errorf("removed member %s listing %s: HTTP %d %s", m.UserID, m.WorkspaceID, r.status, r.body)
		}
		removed++
	}
	if removed == 0 {
		t.Fatal("the dataset has no removed members")
	}
}
