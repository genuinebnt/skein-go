//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
)

// Links typed into notes reach search's graph projection through
// LinksChanged; blast radius and shortest paths then follow them.
func TestBlastRadiusAndShortestPathFollowNewlyTypedLinks(t *testing.T) {
	t.Parallel()
	owner := signUp(t, "linker")
	ws := owner.createWorkspace("Graph " + randomHex(t, 3))
	titles := []string{"Raft basics", "Log replication", "Snapshots", "Membership changes"}
	notes := map[string]*skeinv1.Note{}
	for _, title := range titles {
		notes[title] = owner.createNote(ws, title, "About "+strings.ToLower(title)+".")
	}
	id := func(title string) string { return notes[title].GetId() }

	// Raft basics -> Log replication -> Snapshots, typed live; Membership
	// changes stays unlinked.
	for _, link := range [][2]string{{"Raft basics", "Log replication"}, {"Log replication", "Snapshots"}} {
		r := newReplica(t, owner, id(link[0])).mustConnect()
		eventually(t, longWait, "the replica has "+link[0], func() (bool, string) { return r.length() > 0, r.text() })
		r.appendText(fmt.Sprintf(" See [[%s]].", link[1]))
		r.disconnect()
	}

	var blast skeinv1.BlastRadiusResponse
	eventually(t, longWait, "blast radius follows the typed links", func() (bool, string) {
		owner.call(http.MethodGet, "/v1/notes/"+id("Raft basics")+"/blast-radius?direction=BLAST_DIRECTION_CITES&maxHops=5", nil, &blast)
		h := blast.GetHops()
		return h[id("Log replication")] == 1 && h[id("Snapshots")] == 2, fmt.Sprintf("%v", h)
	})
	if _, ok := blast.GetHops()[id("Membership changes")]; ok {
		t.Errorf("an unlinked note is in the blast radius: %v", blast.GetHops())
	}
	var citedBy skeinv1.BlastRadiusResponse
	owner.call(http.MethodGet, "/v1/notes/"+id("Snapshots")+"/blast-radius?direction=BLAST_DIRECTION_CITED_BY&maxHops=5", nil, &citedBy)
	if citedBy.GetHops()[id("Raft basics")] != 2 || citedBy.GetHops()[id("Log replication")] != 1 {
		t.Errorf("cited-by from Snapshots: %v", citedBy.GetHops())
	}

	var path skeinv1.ShortestPathResponse
	owner.call(http.MethodGet, "/v1/notes/"+id("Raft basics")+"/shortest-path/"+id("Snapshots")+"?metric=PATH_METRIC_HOPS", nil, &path)
	want := []string{id("Raft basics"), id("Log replication"), id("Snapshots")}
	if !path.GetFound() || !slices.Equal(path.GetPath(), want) {
		t.Errorf("shortest path %v (found %v), want %v", path.GetPath(), path.GetFound(), want)
	}
	owner.call(http.MethodGet, "/v1/notes/"+id("Raft basics")+"/shortest-path/"+id("Membership changes")+"?metric=PATH_METRIC_HOPS", nil, &path)
	if path.GetFound() {
		t.Errorf("found a path to an unlinked note: %v", path.GetPath())
	}

	// Removing a link takes it out of the graph again.
	r := newReplica(t, owner, id("Log replication")).mustConnect()
	defer r.disconnect()
	eventually(t, longWait, "the replica has the note", func() (bool, string) {
		return strings.Contains(r.text(), "[[Snapshots]]"), r.text()
	})
	text := r.text()
	at := strings.Index(text, " See [[Snapshots]].")
	r.remove(len([]rune(text[:at])), len([]rune(" See [[Snapshots]].")))
	eventually(t, longWait, "the removed link leaves the graph", func() (bool, string) {
		owner.call(http.MethodGet, "/v1/notes/"+id("Raft basics")+"/blast-radius?direction=BLAST_DIRECTION_CITES&maxHops=5", nil, &blast)
		_, still := blast.GetHops()[id("Snapshots")]
		return !still, fmt.Sprintf("%v", blast.GetHops())
	})
}
