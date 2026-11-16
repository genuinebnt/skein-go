package generate_test

// Black-box tests over generated data. They read files the way a service
// loader would and recompute every piece of ground truth with independent
// code (a recursive Tarjan, a plain BFS, a separate tokenizer and policy
// evaluator), so a bug in the generator's own truth code cannot hide itself.
//
// Most tests read the committed small dataset (testdata/seed/small);
// TestGeneratedSmallIsByteIdenticalToCommitted proves that dataset is exactly
// what the generator produces now.

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/tools/seed/internal/generate"
	"github.com/genuinebnt/skein-go/tools/seed/internal/history"
)

const marker = "\u001e"

func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.work not found above the test directory")
		}
		dir = parent
	}
}

func smallDir(t testing.TB) string { return filepath.Join(repoRoot(t), "testdata", "seed", "small") }

// lines decodes every line of a JSON Lines file into T.
func lines[T any](t testing.TB, dir, rel string) []T {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for n := 1; sc.Scan(); n++ {
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			t.Fatalf("%s line %d: %v", rel, n, err)
		}
		out = append(out, v)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Minimal record shapes, declared here rather than imported so the tests
// read the files as an outside loader would.

type note struct {
	ID, WorkspaceID, Title, OpsPath, OpsSHA256, TextSHA256, CreatedBy string
	Trashed                                                           bool
	TrashedAt                                                         *string
	IsRoot                                                            bool
	BlockCount                                                        int
}

func (n *note) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID          string  `json:"id"`
		WorkspaceID string  `json:"workspace_id"`
		Title       string  `json:"title"`
		OpsPath     string  `json:"ops_path"`
		OpsSHA256   string  `json:"ops_sha256"`
		TextSHA256  string  `json:"text_sha256"`
		CreatedBy   string  `json:"created_by"`
		Trashed     bool    `json:"trashed"`
		TrashedAt   *string `json:"trashed_at"`
		IsRoot      bool    `json:"is_root"`
		BlockCount  int     `json:"block_count"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*n = note{raw.ID, raw.WorkspaceID, raw.Title, raw.OpsPath, raw.OpsSHA256, raw.TextSHA256, raw.CreatedBy, raw.Trashed, raw.TrashedAt, raw.IsRoot, raw.BlockCount}
	return nil
}

type block struct {
	NoteID   string  `json:"note_id"`
	BlockID  string  `json:"block_id"`
	Position int     `json:"position"`
	Type     string  `json:"type"`
	Language *string `json:"language"`
	Text     string  `json:"text"`
}

type final struct {
	NoteID        string `json:"note_id"`
	Text          string `json:"text"`
	VersionVector []struct {
		ReplicaID string `json:"replica_id"`
		Counter   uint64 `json:"counter"`
	} `json:"version_vector"`
}

type batch struct {
	NoteID      string `json:"note_id"`
	Seq         int    `json:"seq"`
	ReplicaID   string `json:"replica_id"`
	UserID      string `json:"user_id"`
	DeliveredAt string `json:"delivered_at"`
	ByteOffset  int    `json:"byte_offset"`
	ByteLength  int    `json:"byte_length"`
	OpCount     int    `json:"op_count"`
}

type link struct {
	WorkspaceID  string  `json:"workspace_id"`
	SourceNoteID string  `json:"source_note_id"`
	TargetTitle  string  `json:"target_title"`
	TargetNoteID *string `json:"target_note_id"`
	BlockID      *string `json:"block_id"`
	RemovedAt    *string `json:"removed_at"`
}

// mustParseTime parses a seed timestamp. t may be nil inside helpers that
// have no test handle; a malformed timestamp then panics.
func mustParseTime(t testing.TB, s string) time.Time {
	v, err := time.Parse("2006-01-02T15:04:05.000Z", s)
	if err != nil {
		if t == nil {
			panic(err)
		}
		t.Helper()
		t.Fatal(err)
	}
	return v
}

// --- determinism -----------------------------------------------------------

func readTree(t testing.TB, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		data, err := os.ReadFile(path)
		out[filepath.ToSlash(rel)] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func generateSmall(t *testing.T, workers int) string {
	t.Helper()
	prof, err := generate.LookupProfile("small")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "small")
	if _, err := generate.Run(generate.Options{Profile: prof, Seed: 1234, Out: dir, Workers: workers}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestGeneratedSmallIsByteIdenticalToCommitted(t *testing.T) {
	a := readTree(t, generateSmall(t, 1))
	b := readTree(t, generateSmall(t, 3))
	committed := readTree(t, smallDir(t))
	for name, want := range map[string]map[string][]byte{"second run with 3 workers": b, "committed testdata/seed/small": committed} {
		if len(want) != len(a) {
			t.Fatalf("%s has %d files, a fresh run has %d (run make seed-small after changing the generator)", name, len(want), len(a))
		}
		for path, data := range a {
			if !bytes.Equal(data, want[path]) {
				t.Fatalf("%s differs from a fresh run at %s (run make seed-small after changing the generator)", name, path)
			}
		}
	}
}

func TestDifferentSeedChangesTheData(t *testing.T) {
	prof, _ := generate.LookupProfile("small")
	dir := filepath.Join(t.TempDir(), "other")
	if _, err := generate.Run(generate.Options{Profile: prof, Seed: 4321, Out: dir}); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(dir, "notes.jsonl"))
	b, _ := os.ReadFile(filepath.Join(smallDir(t), "notes.jsonl"))
	if bytes.Equal(a, b) {
		t.Fatal("seed 4321 produced the same notes as seed 1234")
	}
}

func TestRunRefusesToReplaceANonSeedDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "precious.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	prof, _ := generate.LookupProfile("small")
	if _, err := generate.Run(generate.Options{Profile: prof, Seed: 1, Out: dir}); err == nil {
		t.Fatal("Run replaced a directory without a manifest.json")
	}
	if _, err := os.Stat(filepath.Join(dir, "precious.txt")); err != nil {
		t.Fatal("Run deleted an unrelated file")
	}
}

// --- counts ----------------------------------------------------------------

type manifest struct {
	Profile string           `json:"profile"`
	Seed    uint64           `json:"seed"`
	Counts  map[string]int64 `json:"counts"`
	Files   []struct {
		Path    string `json:"path"`
		Bytes   int64  `json:"bytes"`
		Records int64  `json:"records"`
		SHA256  string `json:"sha256"`
	} `json:"files"`
	Ops struct {
		Files  int    `json:"files"`
		Bytes  int64  `json:"bytes"`
		Digest string `json:"digest"`
	} `json:"ops"`
}

func readManifest(t testing.TB, dir string) manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSmallCountsAreWithinProfileBounds(t *testing.T) {
	m := readManifest(t, smallDir(t))
	prof, _ := generate.LookupProfile("small")
	if m.Counts["users"] != int64(prof.Users) || m.Counts["workspaces"] != int64(prof.Workspaces) || m.Counts["notes"] != int64(prof.Notes) {
		t.Fatalf("counts %v do not match profile %+v", m.Counts, prof)
	}
	blocks := float64(m.Counts["blocks"])
	if d := blocks/float64(prof.Blocks) - 1; d > generate.BlockTolerance || d < -generate.BlockTolerance {
		t.Fatalf("%v blocks, profile target %d ±%.0f%%", blocks, prof.Blocks, generate.BlockTolerance*100)
	}
	for _, file := range []string{"comments.jsonl", "notifications.jsonl", "chats.jsonl", "rejected_ops.jsonl", "truth/discovery/near_duplicates.jsonl",
		"truth/discovery/suggested_links.jsonl", "truth/graph/dangling.jsonl", "truth/agent/evals.jsonl", "share_links.jsonl", "acl.jsonl"} {
		if m.Counts[file] == 0 {
			t.Errorf("%s is empty; every kind of data should appear in the small profile", file)
		}
	}
}

func TestManifestHashesMatchFiles(t *testing.T) {
	dir := smallDir(t)
	m := readManifest(t, dir)
	for _, f := range m.Files {
		data, err := os.ReadFile(filepath.Join(dir, f.Path))
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(data)) != f.Bytes || sha(data) != f.SHA256 {
			t.Errorf("%s: manifest says %d bytes %s, file has %d bytes %s", f.Path, f.Bytes, f.SHA256, len(data), sha(data))
		}
		if strings.HasSuffix(f.Path, ".jsonl") && int64(bytes.Count(data, []byte("\n"))) != f.Records {
			t.Errorf("%s: manifest says %d records", f.Path, f.Records)
		}
	}
	var digest bytes.Buffer
	for _, n := range lines[note](t, dir, "notes.jsonl") {
		data, err := os.ReadFile(filepath.Join(dir, n.OpsPath))
		if err != nil {
			t.Fatal(err)
		}
		if sha(data) != n.OpsSHA256 {
			t.Errorf("%s: ops_sha256 does not match the file", n.OpsPath)
		}
		digest.WriteString(n.OpsPath + " " + n.OpsSHA256 + "\n")
	}
	if sha(digest.Bytes()) != m.Ops.Digest {
		t.Error("manifest ops digest does not match the ops files")
	}
}

// --- CRDT replay -----------------------------------------------------------

func TestReplayingEveryNoteReproducesFinalTextBlocksAndVersion(t *testing.T) {
	dir := smallDir(t)
	finals := map[string]final{}
	for _, f := range lines[final](t, dir, "truth/crdt/final.jsonl") {
		finals[f.NoteID] = f
	}
	blocksOf := map[string][]block{}
	for _, b := range lines[block](t, dir, "blocks.jsonl") {
		blocksOf[b.NoteID] = append(blocksOf[b.NoteID], b)
	}
	batchesOf := map[string][]batch{}
	for _, b := range lines[batch](t, dir, "batches.jsonl") {
		batchesOf[b.NoteID] = append(batchesOf[b.NoteID], b)
	}
	for _, n := range lines[note](t, dir, "notes.jsonl") {
		file, err := os.ReadFile(filepath.Join(dir, n.OpsPath))
		if err != nil {
			t.Fatal(err)
		}
		seq, err := crdt.New(history.ServerReplica)
		if err != nil {
			t.Fatal(err)
		}
		bs := batchesOf[n.ID]
		frames, err := history.UnframeBatches(file)
		if err != nil || len(frames) != len(bs) {
			t.Fatalf("%s: %d frames (%v), %d batch records", n.Title, len(frames), err, len(bs))
		}
		for i, frame := range frames {
			b := bs[i]
			if b.Seq != i || !bytes.Equal(frame, file[b.ByteOffset:b.ByteOffset+b.ByteLength]) {
				t.Fatalf("%s batch %d: offset or length in batches.jsonl does not match the file", n.Title, i)
			}
			ops, err := crdt.DecodeOps(frame)
			if err != nil {
				t.Fatalf("%s batch %d: %v", n.Title, i, err)
			}
			if len(ops) != b.OpCount {
				t.Fatalf("%s batch %d: %d ops, record says %d", n.Title, i, len(ops), b.OpCount)
			}
			for _, op := range ops {
				if uint64(op.ID.Replica) != parseUint(b.ReplicaID) {
					t.Fatalf("%s batch %d: op from replica %d in a batch of %s", n.Title, i, op.ID.Replica, b.ReplicaID)
				}
				if err := seq.Apply(op); err != nil {
					t.Fatalf("%s batch %d: %v", n.Title, i, err)
				}
			}
			if err := seq.CheckInvariants(); err != nil {
				t.Fatalf("%s batch %d: %v", n.Title, i, err)
			}
		}
		f := finals[n.ID]
		text := seq.Text()
		if text != f.Text || sha([]byte(text)) != n.TextSHA256 {
			t.Fatalf("%s: replayed text differs from truth/crdt/final.jsonl", n.Title)
		}
		vv := seq.Version()
		if len(vv.Replicas()) != len(f.VersionVector) {
			t.Fatalf("%s: version vector has %d replicas, truth %d", n.Title, len(vv.Replicas()), len(f.VersionVector))
		}
		for _, e := range f.VersionVector {
			if vv.Get(crdt.ReplicaID(parseUint(e.ReplicaID))) != e.Counter {
				t.Fatalf("%s: version vector differs at replica %s", n.Title, e.ReplicaID)
			}
		}
		// Blocks are exactly the marker-delimited pieces, and each block's
		// ID is the ID of its marker element.
		parts := strings.Split(text, marker)
		blks := blocksOf[n.ID]
		if parts[0] != "" || len(parts)-1 != len(blks) || n.BlockCount != len(blks) {
			t.Fatalf("%s: %d pieces, %d blocks", n.Title, len(parts)-1, len(blks))
		}
		pos := 0
		for i, b := range blks {
			id, err := seq.IDAt(pos)
			if err != nil || id.String() != b.BlockID || b.Position != i || b.Text != parts[i+1] {
				t.Fatalf("%s block %d: id %v (want %s), position %d, text match %v", n.Title, i, id, b.BlockID, b.Position, b.Text == parts[i+1])
			}
			pos += 1 + utf8.RuneCountInString(parts[i+1])
		}
	}
}

func parseUint(s string) uint64 {
	var v uint64
	for _, c := range s {
		v = v*10 + uint64(c-'0')
	}
	return v
}

func TestRejectedBatchesAreValidButRefusedForAccessReasons(t *testing.T) {
	dir := smallDir(t)
	type rejected struct {
		NoteID      string `json:"note_id"`
		UserID      string `json:"user_id"`
		DeliveredAt string `json:"delivered_at"`
		Reason      string `json:"reason"`
		OpsBase64   string `json:"ops_base64"`
	}
	notes := map[string]note{}
	for _, n := range lines[note](t, dir, "notes.jsonl") {
		notes[n.ID] = n
	}
	pol := loadPolicy(t, dir)
	rs := lines[rejected](t, dir, "rejected_ops.jsonl")
	reasons := map[string]int{}
	for _, r := range rs {
		reasons[r.Reason]++
		raw, err := base64.StdEncoding.DecodeString(r.OpsBase64)
		if err != nil {
			t.Fatal(err)
		}
		ops, err := crdt.DecodeOps(raw)
		if err != nil || len(ops) == 0 {
			t.Fatalf("rejected batch does not decode: %v", err)
		}
		// Applied on top of the accepted history the batch is causally
		// ready: the only reason to refuse it is access.
		file, _ := os.ReadFile(filepath.Join(dir, notes[r.NoteID].OpsPath))
		frames, _ := history.UnframeBatches(file)
		seq, _ := crdt.New(history.ServerReplica)
		for _, fr := range frames {
			accepted, _ := crdt.DecodeOps(fr)
			for _, op := range accepted {
				if err := seq.Apply(op); err != nil {
					t.Fatal(err)
				}
			}
		}
		before := seq.Text()
		for _, op := range ops {
			if err := seq.Apply(op); err != nil {
				t.Fatalf("rejected batch is not causally ready on the final state: %v", err)
			}
		}
		if seq.Text() == before {
			t.Fatal("rejected batch would not change the text")
		}
		at := mustParseTime(t, r.DeliveredAt)
		if pol.can(r.UserID, "ACTION_NOTE_EDIT", r.NoteID, at) {
			t.Fatalf("user %s could edit %s at %s, but the batch is rejected (%s)", r.UserID, r.NoteID, r.DeliveredAt, r.Reason)
		}
		removed := pol.removedBy(notes[r.NoteID].WorkspaceID, r.UserID, at)
		if (r.Reason == "access_revoked") != removed {
			t.Fatalf("reason %s but removed=%v", r.Reason, removed)
		}
	}
	if reasons["access_revoked"] == 0 || reasons["insufficient_role"] == 0 {
		t.Fatalf("want both rejection reasons, got %v", reasons)
	}
}

func TestEveryAcceptedBatchWasMadeBySomeoneAllowedToEdit(t *testing.T) {
	dir := smallDir(t)
	pol := loadPolicy(t, dir)
	for _, b := range lines[batch](t, dir, "batches.jsonl") {
		if !pol.can(b.UserID, "ACTION_NOTE_EDIT", b.NoteID, mustParseTime(t, b.DeliveredAt)) {
			t.Fatalf("batch %d of note %s by %s at %s: that user could not edit then", b.Seq, b.NoteID, b.UserID, b.DeliveredAt)
		}
	}
}

func TestConflictScenariosReplayToTheirExpectedText(t *testing.T) {
	type scenario struct {
		Name     string            `json:"name"`
		Expect   map[string]string `json:"expect"`
		Replicas map[string]uint64 `json:"replicas"`
		Batches  []struct {
			OpsBase64 string `json:"ops_base64"`
			Rejected  bool   `json:"rejected"`
		} `json:"batches"`
	}
	for _, sc := range lines[scenario](t, smallDir(t), "truth/crdt/conflicts.jsonl") {
		seq, _ := crdt.New(history.ServerReplica)
		for _, b := range sc.Batches {
			if b.Rejected {
				continue
			}
			raw, _ := base64.StdEncoding.DecodeString(b.OpsBase64)
			ops, err := crdt.DecodeOps(raw)
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range ops {
				if err := seq.Apply(op); err != nil {
					t.Fatalf("%s: %v", sc.Name, err)
				}
			}
			if err := seq.CheckInvariants(); err != nil {
				t.Fatal(err)
			}
		}
		want, ok := sc.Expect["all"]
		if !ok {
			want = sc.Expect["A"]
		}
		if seq.Text() != want {
			t.Fatalf("%s: server replay gives %q, want %q", sc.Name, seq.Text(), want)
		}
	}
}

// --- graph -----------------------------------------------------------------

// tarjan is a plain recursive Tarjan, written independently of the
// generator's iterative one.
func tarjan(n int, adj [][]int) [][]int {
	index, low := make([]int, n), make([]int, n)
	on := make([]bool, n)
	for i := range index {
		index[i] = -1
	}
	var stack []int
	var out [][]int
	next := 0
	var visit func(v int)
	visit = func(v int) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		on[v] = true
		for _, w := range adj[v] {
			if index[w] < 0 {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var c []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[w] = false
				c = append(c, w)
				if w == v {
					break
				}
			}
			out = append(out, c)
		}
	}
	for v := 0; v < n; v++ {
		if index[v] < 0 {
			visit(v)
		}
	}
	return out
}

type wsGraph struct {
	ids  []string
	idx  map[string]int
	out  [][]int
	in   [][]int
	root map[int]bool
}

func loadGraphs(t testing.TB, dir string) map[string]*wsGraph {
	t.Helper()
	gs := map[string]*wsGraph{}
	live := map[string]bool{}
	for _, n := range lines[note](t, dir, "notes.jsonl") {
		if n.Trashed {
			continue
		}
		g := gs[n.WorkspaceID]
		if g == nil {
			g = &wsGraph{idx: map[string]int{}, root: map[int]bool{}}
			gs[n.WorkspaceID] = g
		}
		g.idx[n.ID] = len(g.ids)
		if n.IsRoot {
			g.root[len(g.ids)] = true
		}
		g.ids = append(g.ids, n.ID)
		live[n.ID] = true
	}
	for _, g := range gs {
		g.out, g.in = make([][]int, len(g.ids)), make([][]int, len(g.ids))
	}
	seen := map[[2]string]bool{}
	for _, l := range lines[link](t, dir, "links.jsonl") {
		if l.RemovedAt != nil || l.TargetNoteID == nil || !live[l.SourceNoteID] || !live[*l.TargetNoteID] {
			continue
		}
		k := [2]string{l.SourceNoteID, *l.TargetNoteID}
		if seen[k] {
			continue
		}
		seen[k] = true
		g := gs[l.WorkspaceID]
		u, v := g.idx[l.SourceNoteID], g.idx[*l.TargetNoteID]
		g.out[u] = append(g.out[u], v)
		g.in[v] = append(g.in[v], u)
	}
	return gs
}

func TestLinksFileMatchesTheLinksInBlockText(t *testing.T) {
	dir := smallDir(t)
	pat := regexp.MustCompile(`\[\[([^\[\]\n]+)\]\]`)
	want := map[string]bool{}
	for _, b := range lines[block](t, dir, "blocks.jsonl") {
		if b.Type == "code" {
			continue
		}
		for _, m := range pat.FindAllStringSubmatch(b.Text, -1) {
			want[b.NoteID+"\x00"+m[1]] = true
		}
	}
	got := map[string]bool{}
	for _, l := range lines[link](t, dir, "links.jsonl") {
		if l.RemovedAt == nil {
			got[l.SourceNoteID+"\x00"+l.TargetTitle] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("links.jsonl has %d current links, block text has %d", len(got), len(want))
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("link %q in text but not in links.jsonl", k)
		}
	}
}

func TestPlantedSCCsAreExactlyTheStronglyConnectedComponents(t *testing.T) {
	dir := smallDir(t)
	gs := loadGraphs(t, dir)
	type sccRec struct {
		WorkspaceID string   `json:"workspace_id"`
		Members     []string `json:"members"`
		Planted     *string  `json:"planted"`
	}
	want := map[string]map[string]bool{}
	planted := 0
	for _, s := range lines[sccRec](t, dir, "truth/graph/sccs.jsonl") {
		if want[s.WorkspaceID] == nil {
			want[s.WorkspaceID] = map[string]bool{}
		}
		want[s.WorkspaceID][strings.Join(s.Members, ",")] = true
		if s.Planted != nil {
			planted++
		}
	}
	if planted == 0 {
		t.Fatal("no planted SCCs in the truth")
	}
	for ws, g := range gs {
		got := map[string]bool{}
		for _, c := range tarjan(len(g.ids), g.out) {
			if len(c) < 2 {
				continue
			}
			var ids []string
			for _, v := range c {
				ids = append(ids, g.ids[v])
			}
			sort.Strings(ids)
			got[strings.Join(ids, ",")] = true
		}
		if len(got) != len(want[ws]) {
			t.Fatalf("workspace %s: Tarjan finds %d non-trivial SCCs, truth lists %d", ws, len(got), len(want[ws]))
		}
		for k := range got {
			if !want[ws][k] {
				t.Fatalf("workspace %s: SCC %s missing from truth", ws, k)
			}
		}
	}
}

func bfs(adj [][]int, src int) []int {
	dist := make([]int, len(adj))
	for i := range dist {
		dist[i] = -1
	}
	dist[src] = 0
	q := []int{src}
	for len(q) > 0 {
		v := q[0]
		q = q[1:]
		for _, w := range adj[v] {
			if dist[w] < 0 {
				dist[w] = dist[v] + 1
				q = append(q, w)
			}
		}
	}
	return dist
}

func TestBlastRadiusAndShortestPathsMatchBFS(t *testing.T) {
	dir := smallDir(t)
	gs := loadGraphs(t, dir)
	type blast struct {
		WorkspaceID string         `json:"workspace_id"`
		NoteID      string         `json:"note_id"`
		Direction   string         `json:"direction"`
		RingSizes   []int          `json:"ring_sizes"`
		Hops        map[string]int `json:"hops"`
	}
	for _, b := range lines[blast](t, dir, "truth/graph/blast_radius.jsonl") {
		g := gs[b.WorkspaceID]
		adj := g.out
		if b.Direction == "inbound" {
			adj = g.in
		}
		dist := bfs(adj, g.idx[b.NoteID])
		var rings []int
		for v, d := range dist {
			if d < 0 {
				continue
			}
			for len(rings) <= d {
				rings = append(rings, 0)
			}
			rings[d]++
			if b.Hops != nil && b.Hops[g.ids[v]] != d {
				t.Fatalf("%s %s: hop of %s is %d, truth %d", b.NoteID, b.Direction, g.ids[v], d, b.Hops[g.ids[v]])
			}
		}
		if len(rings) != len(b.RingSizes) {
			t.Fatalf("%s %s: rings %v, truth %v", b.NoteID, b.Direction, rings, b.RingSizes)
		}
		for i := range rings {
			if rings[i] != b.RingSizes[i] {
				t.Fatalf("%s %s: rings %v, truth %v", b.NoteID, b.Direction, rings, b.RingSizes)
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
	}
	unreachable := 0
	for _, p := range lines[path](t, dir, "truth/graph/shortest_paths.jsonl") {
		g := gs[p.WorkspaceID]
		undirected := make([][]int, len(g.ids))
		for v := range g.out {
			undirected[v] = append(append(undirected[v], g.out[v]...), g.in[v]...)
		}
		d := bfs(undirected, g.idx[p.From])[g.idx[p.To]]
		if (d >= 0) != p.Found || (p.Found && d != p.Distance) {
			t.Fatalf("path %s -> %s: BFS distance %d, truth found=%v distance=%d", p.From, p.To, d, p.Found, p.Distance)
		}
		if !p.Found {
			unreachable++
			continue
		}
		if len(p.Path) != d+1 || p.Path[0] != p.From || p.Path[d] != p.To {
			t.Fatalf("path %s -> %s: bad endpoints or length", p.From, p.To)
		}
		for i := 0; i+1 < len(p.Path); i++ {
			u, v := g.idx[p.Path[i]], g.idx[p.Path[i+1]]
			if !contains(g.out[u], v) && !contains(g.out[v], u) {
				t.Fatalf("path %s -> %s: step %d is not a link", p.From, p.To, i)
			}
		}
	}
	if unreachable == 0 {
		t.Error("no unreachable pair in the shortest path truth; orphans should give one")
	}
}

func contains(xs []int, x int) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestComponentsOrphansCyclesAndHubsAreConsistent(t *testing.T) {
	dir := smallDir(t)
	gs := loadGraphs(t, dir)
	type comp struct {
		WorkspaceID string   `json:"workspace_id"`
		HasRoot     bool     `json:"has_root"`
		Orphan      bool     `json:"orphan"`
		Planted     bool     `json:"planted"`
		Members     []string `json:"members"`
	}
	counted := map[string]int{}
	plantedOrphans := 0
	for _, c := range lines[comp](t, dir, "truth/graph/components.jsonl") {
		g := gs[c.WorkspaceID]
		counted[c.WorkspaceID] += len(c.Members)
		in := map[int]bool{}
		hasRoot := false
		for _, id := range c.Members {
			in[g.idx[id]] = true
			hasRoot = hasRoot || g.root[g.idx[id]]
		}
		for v := range in {
			for _, w := range append(append([]int{}, g.out[v]...), g.in[v]...) {
				if !in[w] {
					t.Fatalf("component in %s is not closed: %s links outside it", c.WorkspaceID, g.ids[v])
				}
			}
		}
		if hasRoot != c.HasRoot || c.Orphan == c.HasRoot {
			t.Fatalf("component root flags wrong in %s", c.WorkspaceID)
		}
		if c.Planted {
			plantedOrphans++
		}
	}
	for ws, g := range gs {
		if counted[ws] != len(g.ids) {
			t.Fatalf("components of %s cover %d notes of %d", ws, counted[ws], len(g.ids))
		}
	}
	if plantedOrphans == 0 {
		t.Error("no planted orphan component was found in the final graph")
	}
	type cycle struct {
		WorkspaceID string   `json:"workspace_id"`
		Cycle       []string `json:"cycle"`
	}
	for _, c := range lines[cycle](t, dir, "truth/graph/cycles.jsonl") {
		g := gs[c.WorkspaceID]
		if c.Cycle[0] != c.Cycle[len(c.Cycle)-1] {
			t.Fatal("cycle does not end where it starts")
		}
		for i := 0; i+1 < len(c.Cycle); i++ {
			if !contains(g.out[g.idx[c.Cycle[i]]], g.idx[c.Cycle[i+1]]) {
				t.Fatalf("cycle step %s -> %s is not a link", c.Cycle[i], c.Cycle[i+1])
			}
		}
	}
	type hub struct {
		WorkspaceID string `json:"workspace_id"`
		NoteID      string `json:"note_id"`
		InDegree    int    `json:"in_degree"`
		OutDegree   int    `json:"out_degree"`
	}
	for _, h := range lines[hub](t, dir, "truth/graph/hubs.jsonl") {
		g := gs[h.WorkspaceID]
		v := g.idx[h.NoteID]
		if len(g.in[v]) != h.InDegree || len(g.out[v]) != h.OutDegree {
			t.Fatalf("hub %s: degrees %d/%d, truth %d/%d", h.NoteID, len(g.in[v]), len(g.out[v]), h.InDegree, h.OutDegree)
		}
	}
	type dangling struct {
		SourceNoteID string `json:"source_note_id"`
		TargetTitle  string `json:"target_title"`
	}
	want := map[string]bool{}
	for _, l := range lines[link](t, dir, "links.jsonl") {
		if l.RemovedAt == nil && l.TargetNoteID == nil {
			want[l.SourceNoteID+l.TargetTitle] = true
		}
	}
	ds := lines[dangling](t, dir, "truth/graph/dangling.jsonl")
	if len(ds) != len(want) {
		t.Fatalf("%d dangling links in truth, %d in links.jsonl", len(ds), len(want))
	}
}

// --- search ----------------------------------------------------------------

// tokens is the search tokenizer from docs/seed-data.md, written again.
func tokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r))
	}) {
		out[f] = true
	}
	return out
}

func hasTerm(text string, toks map[string]bool, term string) bool {
	for _, r := range term {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return strings.Contains(strings.ToLower(text), term)
		}
	}
	return toks[term]
}

func allTerms(text string, terms []string) bool {
	toks := tokens(text)
	for _, term := range terms {
		if !hasTerm(text, toks, term) {
			return false
		}
	}
	return true
}

func TestTextQrelsMatchAnExhaustiveScan(t *testing.T) {
	dir := smallDir(t)
	pol := loadPolicy(t, dir)
	type query struct {
		ID            string   `json:"id"`
		WorkspaceID   string   `json:"workspace_id"`
		Terms         []string `json:"terms"`
		SubjectUserID string   `json:"subject_user_id"`
		Qrels         []struct {
			NoteID string `json:"note_id"`
			Grade  int    `json:"grade"`
		} `json:"qrels"`
	}
	notes := lines[note](t, dir, "notes.jsonl")
	blocksOf := map[string][]block{}
	for _, b := range lines[block](t, dir, "blocks.jsonl") {
		blocksOf[b.NoteID] = append(blocksOf[b.NoteID], b)
	}
	ref := mustParseTime(t, "2026-11-01T00:00:00.000Z")
	nonEmpty, permissionFiltered := 0, 0
	for _, q := range lines[query](t, dir, "truth/search/text_queries.jsonl") {
		want := map[string]int{}
		for _, n := range notes {
			if n.WorkspaceID != q.WorkspaceID || n.Trashed || !pol.can(q.SubjectUserID, "ACTION_NOTE_READ", n.ID, ref) {
				continue
			}
			all := n.Title
			for _, b := range blocksOf[n.ID] {
				all += "\n" + b.Text
			}
			if !allTerms(all, q.Terms) {
				continue
			}
			grade := 1
			for _, b := range blocksOf[n.ID] {
				if allTerms(b.Text, q.Terms) {
					grade = 2
				}
			}
			if allTerms(n.Title, q.Terms) {
				grade = 3
			}
			want[n.ID] = grade
		}
		if len(want) != len(q.Qrels) {
			t.Fatalf("%s: scan finds %d relevant notes, truth has %d", q.ID, len(want), len(q.Qrels))
		}
		for _, qr := range q.Qrels {
			if want[qr.NoteID] != qr.Grade {
				t.Fatalf("%s: note %s grade %d, scan says %d", q.ID, qr.NoteID, qr.Grade, want[qr.NoteID])
			}
		}
		if len(want) > 0 {
			nonEmpty++
		}
		if strings.Contains(q.ID, "guest") {
			permissionFiltered++
		}
	}
	if nonEmpty < 10 || permissionFiltered == 0 {
		t.Fatalf("%d queries have relevant notes, %d are permission-filtered; want more of both", nonEmpty, permissionFiltered)
	}
}

func TestRegexMatchesAreExactlyWhatRE2Finds(t *testing.T) {
	dir := smallDir(t)
	type query struct {
		ID            string   `json:"id"`
		WorkspaceID   string   `json:"workspace_id"`
		Pattern       string   `json:"pattern"`
		CaseSensitive bool     `json:"case_sensitive"`
		CodeOnly      bool     `json:"code_only"`
		Languages     []string `json:"languages"`
		Matches       []struct {
			BlockID string `json:"block_id"`
			Start   int    `json:"start"`
			End     int    `json:"end"`
			Text    string `json:"text"`
		} `json:"matches"`
	}
	wsOf := map[string]string{}
	for _, n := range lines[note](t, dir, "notes.jsonl") {
		if !n.Trashed {
			wsOf[n.ID] = n.WorkspaceID
		}
	}
	bs := lines[block](t, dir, "blocks.jsonl")
	total := 0
	for _, q := range lines[query](t, dir, "truth/search/regex_queries.jsonl") {
		pat := q.Pattern
		if !q.CaseSensitive {
			pat = "(?i)" + pat
		}
		re := regexp.MustCompile(pat)
		var got []string
		for _, b := range bs {
			if wsOf[b.NoteID] != q.WorkspaceID {
				continue
			}
			if (q.CodeOnly || len(q.Languages) > 0) && b.Type != "code" {
				continue
			}
			if len(q.Languages) > 0 && (b.Language == nil || !containsStr(q.Languages, *b.Language)) {
				continue
			}
			for _, m := range re.FindAllStringIndex(b.Text, -1) {
				s := utf8.RuneCountInString(b.Text[:m[0]])
				got = append(got, b.BlockID+"@"+itoa(s)+":"+itoa(s+utf8.RuneCountInString(b.Text[m[0]:m[1]])))
			}
		}
		if len(got) != len(q.Matches) {
			t.Fatalf("%s in %s: RE2 finds %d matches, truth lists %d", q.ID, q.WorkspaceID, len(got), len(q.Matches))
		}
		for i, m := range q.Matches {
			if got[i] != m.BlockID+"@"+itoa(m.Start)+":"+itoa(m.End) {
				t.Fatalf("%s match %d: %s vs %s@%d:%d", q.ID, i, got[i], m.BlockID, m.Start, m.End)
			}
		}
		total += len(got)
	}
	if total == 0 {
		t.Fatal("no regex query has any match")
	}
}

func containsStr(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			c := 1
			if ra[i-1] == rb[j-1] {
				c = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+c)
		}
	}
	return d[len(ra)][len(rb)]
}

func TestTitlePrefixesAndTyposAreConsistent(t *testing.T) {
	dir := smallDir(t)
	type prefix struct {
		WorkspaceID    string  `json:"workspace_id"`
		Prefix         string  `json:"prefix"`
		Typo           bool    `json:"typo"`
		IntendedNoteID *string `json:"intended_note_id"`
		EditDistance   int     `json:"edit_distance"`
		Expected       []struct {
			NoteID string `json:"note_id"`
		} `json:"expected"`
	}
	notes := lines[note](t, dir, "notes.jsonl")
	byID := map[string]note{}
	for _, n := range notes {
		byID[n.ID] = n
	}
	for _, p := range lines[prefix](t, dir, "truth/search/title_prefixes.jsonl") {
		want := 0
		for _, n := range notes {
			if n.WorkspaceID == p.WorkspaceID && !n.Trashed && strings.HasPrefix(strings.ToLower(n.Title), strings.ToLower(p.Prefix)) {
				want++
			}
		}
		if want != len(p.Expected) {
			t.Fatalf("prefix %q: %d titles start with it, truth lists %d", p.Prefix, want, len(p.Expected))
		}
		if p.Typo {
			title := []rune(strings.ToLower(byID[*p.IntendedNoteID].Title))
			orig := string(title[:min(len(title), utf8.RuneCountInString(p.Prefix)+1)])
			best := 99
			for l := 1; l <= len([]rune(orig)); l++ {
				best = min(best, levenshtein(strings.ToLower(p.Prefix), string([]rune(orig)[:l])))
			}
			if best > p.EditDistance || p.EditDistance == 0 || p.EditDistance > 2 {
				t.Fatalf("typo prefix %q: distance %d to the intended title's prefix, truth %d", p.Prefix, best, p.EditDistance)
			}
		}
	}
	type typo struct {
		Query        string `json:"query"`
		Intended     string `json:"intended"`
		EditDistance int    `json:"edit_distance"`
	}
	for _, q := range lines[typo](t, dir, "truth/search/typo_queries.jsonl") {
		if d := levenshtein(q.Query, q.Intended); d != q.EditDistance || d == 0 {
			t.Fatalf("typo %q -> %q: distance %d, truth %d", q.Query, q.Intended, d, q.EditDistance)
		}
	}
}

// --- discovery, agent, notifications ----------------------------------------

func TestSuggestedLinkPairsAreUnlinkedAndDuplicatesAreSimilar(t *testing.T) {
	dir := smallDir(t)
	linked := map[[2]string]bool{}
	for _, l := range lines[link](t, dir, "links.jsonl") {
		if l.RemovedAt == nil && l.TargetNoteID != nil {
			linked[[2]string{l.SourceNoteID, *l.TargetNoteID}] = true
		}
	}
	type pair struct {
		A           string   `json:"a"`
		B           string   `json:"b"`
		SharedTerms []string `json:"shared_terms"`
	}
	text := map[string]string{}
	for _, b := range lines[block](t, dir, "blocks.jsonl") {
		text[b.NoteID] += " " + b.Text
	}
	for _, p := range lines[pair](t, dir, "truth/discovery/suggested_links.jsonl") {
		if linked[[2]string{p.A, p.B}] || linked[[2]string{p.B, p.A}] {
			t.Fatalf("suggested pair %s %s is already linked", p.A, p.B)
		}
		for _, term := range p.SharedTerms {
			if !tokens(text[p.A])[term] || !tokens(text[p.B])[term] {
				t.Fatalf("shared term %q is missing from one of %s, %s", term, p.A, p.B)
			}
		}
	}
	type dup struct {
		Original  string  `json:"original_note_id"`
		Duplicate string  `json:"duplicate_note_id"`
		Jaccard   float64 `json:"jaccard_3_shingles"`
	}
	for _, d := range lines[dup](t, dir, "truth/discovery/near_duplicates.jsonl") {
		if d.Jaccard < 0.6 || d.Jaccard >= 1 {
			t.Fatalf("near-duplicate %s has Jaccard %.3f; expected similar but edited", d.Duplicate, d.Jaccard)
		}
	}
}

func TestAgentEvalCitationsQuoteTheirBlocks(t *testing.T) {
	dir := smallDir(t)
	text := map[string]string{}
	for _, b := range lines[block](t, dir, "blocks.jsonl") {
		text[b.BlockID+"/"+b.NoteID] = b.Text
	}
	type eval struct {
		ID        string `json:"id"`
		Outcome   string `json:"outcome"`
		Citations []struct {
			NoteID  string `json:"note_id"`
			BlockID string `json:"block_id"`
			Start   int    `json:"start"`
			End     int    `json:"end"`
			Quote   string `json:"quote"`
		} `json:"citations"`
	}
	es := lines[eval](t, dir, "truth/agent/evals.jsonl")
	outcomes := map[string]int{}
	for _, e := range es {
		outcomes[e.Outcome]++
		for _, c := range e.Citations {
			rs := []rune(text[c.BlockID+"/"+c.NoteID])
			if c.End > len(rs) || string(rs[c.Start:c.End]) != c.Quote {
				t.Fatalf("%s: citation does not quote block %s at %d:%d", e.ID, c.BlockID, c.Start, c.End)
			}
		}
	}
	if len(es) < 20 || len(es) > 30 || outcomes["refuse"] == 0 || outcomes["confirm"] == 0 || outcomes["answer"] == 0 || outcomes["act"] == 0 {
		t.Fatalf("%d evals with outcomes %v; want 20 to 30 covering every outcome", len(es), outcomes)
	}
}

func TestMentionsNotifyExactlyTheMentionedReaders(t *testing.T) {
	dir := smallDir(t)
	pol := loadPolicy(t, dir)
	type comment struct {
		ID        string   `json:"id"`
		NoteID    string   `json:"note_id"`
		AuthorID  string   `json:"author_id"`
		Mentions  []string `json:"mentions"`
		CreatedAt string   `json:"created_at"`
	}
	type notif struct {
		UserID string `json:"user_id"`
		Kind   string `json:"kind"`
		Source struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"source"`
	}
	got := map[string]bool{}
	for _, n := range lines[notif](t, dir, "notifications.jsonl") {
		if n.Kind == "NOTIFICATION_KIND_MENTION" {
			got[n.Source.ID+"/"+n.UserID] = true
		}
	}
	silent := 0
	want := 0
	for _, c := range lines[comment](t, dir, "comments.jsonl") {
		for _, u := range c.Mentions {
			readable := pol.can(u, "ACTION_NOTE_READ", c.NoteID, mustParseTime(t, c.CreatedAt))
			if readable && u != c.AuthorID {
				want++
				if !got[c.ID+"/"+u] {
					t.Fatalf("comment %s mentions %s, who can read the note, but no notification", c.ID, u)
				}
			} else if got[c.ID+"/"+u] {
				t.Fatalf("comment %s notified %s, who cannot read the note", c.ID, u)
			} else {
				silent++
			}
		}
	}
	if want == 0 {
		t.Fatal("no mention notifications at all")
	}
	_ = silent
}

// --- schemas ---------------------------------------------------------------

func sha(b []byte) string { return shaHex(b) }

// mediumBudget is the stated time budget for generating the medium profile
// (8,000 notes, about 150,000 blocks, 16 million CRDT ops, every note
// replay-verified). Measured: 38 to 50 s on a shared 2-core cloud VM.
const mediumBudget = 180 * time.Second

func TestMediumProfileGeneratesWithinBudget(t *testing.T) {
	if os.Getenv("SKEIN_SEED_MEDIUM") != "1" {
		t.Skip("set SKEIN_SEED_MEDIUM=1 (make seed-check does) to generate the medium profile, about 600 MB")
	}
	prof, _ := generate.LookupProfile("medium")
	dir := filepath.Join(t.TempDir(), "medium")
	start := time.Now()
	sum, err := generate.Run(generate.Options{Profile: prof, Seed: 1234, Out: dir})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	t.Logf("medium: %d notes, %d blocks, %d ops, %.0f MB in %s", sum.Counts["notes"], sum.Counts["blocks"], sum.Counts["ops"], float64(sum.Bytes)/1e6, elapsed)
	if elapsed > mediumBudget {
		t.Fatalf("medium took %s, budget %s", elapsed, mediumBudget)
	}
	if d := float64(sum.Counts["blocks"])/float64(prof.Blocks) - 1; d > generate.BlockTolerance || d < -generate.BlockTolerance {
		t.Fatalf("%d blocks, target %d", sum.Counts["blocks"], prof.Blocks)
	}
}
