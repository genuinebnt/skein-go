// Package generate produces Skein's seed datasets: users, workspaces, notes
// built through real CRDT edit histories (libs/crdt), activity, and ground
// truth for every subsystem that tests against them. The output layout and
// the contract for service loaders are in docs/seed-data.md; record shapes
// are pinned by the JSON Schemas in testdata/seed/schema.
//
// # Pipeline
//
//  1. Plan (sequential, in memory): people, roles, note metadata, the link
//     graph with planted structure, search and discovery plants.
//  2. Realize (parallel): each note's content is written, typed through
//     edit sessions on several replicas, replayed through a fresh replica
//     to verify the ops reproduce the text, and turned into records.
//  3. Write (one goroutine, in note order): records stream to JSON Lines
//     files; only small per-note summaries are kept (link targets, a
//     64-dimension vector, search hits), so memory stays bounded.
//  4. Truth: graph, search, discovery, clustering, permissions, CRDT and
//     agent ground truth, computed from the final data, never from the
//     plan alone, and cross-checked against what was planted.
//  5. Manifest: counts and the SHA-256 of every file.
//
// # Determinism
//
// Every random choice draws from a stream named by purpose and index
// (internal/prng), workers only read the plan, and output order is note
// order, so the same profile and seed give byte-identical output whatever
// the worker count. Wall-clock time never enters the output.
package generate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/tools/seed/internal/hashembed"
	"github.com/genuinebnt/skein-go/tools/seed/internal/jsonl"
)

// Options configure a run.
type Options struct {
	Profile Profile
	Seed    uint64
	Out     string
	// Workers is the number of parallel note workers; 0 means one per CPU.
	Workers int
	// Log receives progress lines (never part of the output). Nil discards.
	Log io.Writer
}

// Summary reports what a run produced.
type Summary struct {
	Counts   map[string]int64
	Bytes    int64
	Duration time.Duration
}

// ErrOutputNotEmpty means the output directory holds files that are not a
// previous seed dataset, which the generator refuses to delete.
var ErrOutputNotEmpty = errors.New("generate: output directory is not empty and has no manifest.json")

// outputs owns every open output file.
type outputs struct {
	root    string
	writers map[string]*jsonl.Writer
	order   []string
	files   []jsonl.FileInfo
	opsN    int
	opsB    int64
	opsHash hash.Hash
}

func (o *outputs) write(rel string, rec any) error {
	w, ok := o.writers[rel]
	if !ok {
		var err error
		if w, err = jsonl.Create(o.root, rel); err != nil {
			return err
		}
		o.writers[rel] = w
		o.order = append(o.order, rel)
	}
	return w.Write(rec)
}

func (o *outputs) document(rel string, doc any) error {
	info, err := jsonl.WriteDocument(o.root, rel, doc)
	if err == nil {
		o.files = append(o.files, info)
	}
	return err
}

func (o *outputs) writeOps(rel string, data []byte) error {
	info, err := jsonl.WriteBytes(o.root, rel, data, 0)
	if err != nil {
		return err
	}
	o.opsN++
	o.opsB += info.Bytes
	fmt.Fprintf(o.opsHash, "%s %s\n", info.Path, info.SHA256)
	return nil
}

func (o *outputs) closeAll() error {
	for _, rel := range o.order {
		info, err := o.writers[rel].Close()
		if err != nil {
			return err
		}
		o.files = append(o.files, info)
	}
	o.writers = map[string]*jsonl.Writer{}
	o.order = nil
	return nil
}

// aggregate is the per-note state kept for truth computation.
type aggregate struct {
	p        *plan
	targets  [][]int
	vecs     [][hashembed.Dim]float32
	qrels    [][][]Qrel
	matches  [][][]regexMatch
	shingles map[int][]uint64
	kept     map[int][]finalBlock
	dangling [][]DanglingRecord
	counts   map[string]int64
}

// Every file the generator can write, so that a profile which happens to
// produce no records for one still ships it (empty), and loaders can rely
// on the layout.
var allFiles = []string{
	"users.jsonl", "workspaces.jsonl", "memberships.jsonl", "replicas.jsonl", "notes.jsonl", "blocks.jsonl", "links.jsonl",
	"tags.jsonl", "acl.jsonl", "share_links.jsonl", "comments.jsonl", "notifications.jsonl", "chats.jsonl", "batches.jsonl",
	"rejected_ops.jsonl", "truth/crdt/final.jsonl", "truth/crdt/conflicts.jsonl", "truth/clustering/note_topics.jsonl",
	"truth/clustering/block_embeddings.jsonl", "truth/graph/summary.jsonl", "truth/graph/sccs.jsonl", "truth/graph/cycles.jsonl",
	"truth/graph/components.jsonl", "truth/graph/hubs.jsonl", "truth/graph/blast_radius.jsonl", "truth/graph/shortest_paths.jsonl",
	"truth/graph/communities.jsonl", "truth/graph/bridges.jsonl", "truth/graph/dangling.jsonl", "truth/search/text_queries.jsonl",
	"truth/search/typo_queries.jsonl", "truth/search/regex_queries.jsonl", "truth/search/title_prefixes.jsonl",
	"truth/discovery/suggested_links.jsonl", "truth/discovery/near_duplicates.jsonl", "truth/permissions/matrix.jsonl",
	"truth/agent/evals.jsonl",
}

// checkOut refuses to replace a directory that is not a seed dataset.
func checkOut(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(entries) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		return fmt.Errorf("%w: %s", ErrOutputNotEmpty, dir)
	}
	return nil
}

// Run generates one dataset. It writes into a temporary sibling directory
// and replaces opt.Out only when generation succeeded, so a failed run never
// leaves a half-written dataset behind.
func Run(opt Options) (Summary, error) {
	dir := filepath.Clean(opt.Out)
	if err := checkOut(dir); err != nil {
		return Summary{}, err
	}
	tmp := dir + ".partial"
	if err := os.RemoveAll(tmp); err != nil {
		return Summary{}, err
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return Summary{}, err
	}
	inner := opt
	inner.Out = tmp
	sum, err := run(inner)
	if err != nil {
		os.RemoveAll(tmp)
		return Summary{}, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return Summary{}, err
	}
	return sum, os.Rename(tmp, dir)
}

func run(opt Options) (Summary, error) {
	start := time.Now()
	logf := func(format string, args ...any) {
		if opt.Log != nil {
			fmt.Fprintf(opt.Log, "[%6.1fs] "+format+"\n", append([]any{time.Since(start).Seconds()}, args...)...)
		}
	}
	p, err := newPlan(opt.Profile, opt.Seed)
	if err != nil {
		return Summary{}, err
	}
	logf("planned %d users, %d workspaces, %d notes", len(p.users), len(p.workspaces), len(p.notes))
	o := &outputs{root: opt.Out, writers: map[string]*jsonl.Writer{}, opsHash: sha256.New()}
	for _, f := range allFiles {
		w, err := jsonl.Create(o.root, f)
		if err != nil {
			return Summary{}, err
		}
		o.writers[f] = w
		o.order = append(o.order, f)
	}
	a := &aggregate{p: p, targets: make([][]int, len(p.notes)), vecs: make([][hashembed.Dim]float32, len(p.notes)),
		shingles: map[int][]uint64{}, kept: map[int][]finalBlock{}, dangling: make([][]DanglingRecord, len(p.workspaces)), counts: map[string]int64{}}
	for _, w := range p.workspaces {
		a.qrels = append(a.qrels, make([][]Qrel, len(w.Queries)))
		a.matches = append(a.matches, make([][]regexMatch, len(w.Regexes)))
		for i := range w.Regexes {
			w.Regexes[i].compiled() // compile before workers share the plan
		}
	}
	if err := writePlanRecords(p, o); err != nil {
		return Summary{}, err
	}
	if err := realizeAll(p, a, o, opt.Workers, logf); err != nil {
		return Summary{}, err
	}
	logf("notes written; computing ground truth")
	if err := writeTruth(a, o); err != nil {
		return Summary{}, err
	}
	if err := o.closeAll(); err != nil {
		return Summary{}, err
	}
	man := manifest(p, a, o)
	if err := o.document("manifest.json", man); err != nil {
		return Summary{}, err
	}
	var total int64
	for _, f := range o.files {
		total += f.Bytes
	}
	total += o.opsB
	logf("done: %d files plus %d ops files, %.1f MB", len(o.files), o.opsN, float64(total)/1e6)
	return Summary{Counts: man.Counts, Bytes: total, Duration: time.Since(start)}, nil
}

// writePlanRecords writes the records that come straight from the plan.
func writePlanRecords(p *plan, o *outputs) error {
	for i, u := range p.users {
		rec := UserRecord{ID: u.ID, Email: u.Email, DisplayName: u.Name, Handle: u.Handle, Locale: u.Locale, TimeZone: u.TZ, Company: u.Company,
			CreatedAt: ts(u.CreatedAt), CastKey: strPtr(u.CastKey)}
		if err := o.write("users.jsonl", rec); err != nil {
			return err
		}
		for _, d := range u.Devices {
			dev := d.Device
			if err := o.write("replicas.jsonl", ReplicaRecord{ReplicaID: replicaString(d.Replica), UserID: p.users[i].ID, Kind: "device", Device: &dev, RegisteredAt: ts(d.At)}); err != nil {
				return err
			}
		}
	}
	for _, w := range p.workspaces {
		if err := o.write("workspaces.jsonl", WorkspaceRecord{ID: w.ID, Name: w.Name, Slug: w.Slug, Kind: w.Kind, OwnerID: p.users[w.Owner].ID, CreatedAt: ts(w.CreatedAt)}); err != nil {
			return err
		}
		for _, m := range w.Members {
			rec := MembershipRecord{WorkspaceID: w.ID, UserID: p.users[m.User].ID, Role: m.Role.String(), JoinedAt: ts(m.JoinedAt), RemovedAt: tsPtr(m.RemovedAt)}
			if m.InvitedBy >= 0 {
				rec.InvitedBy = &p.users[m.InvitedBy].ID
			}
			if err := o.write("memberships.jsonl", rec); err != nil {
				return err
			}
		}
	}
	for _, ov := range p.overrides {
		if err := o.write("acl.jsonl", ACLRecord{NoteID: p.notes[ov.Note].ID, UserID: p.users[ov.User].ID, Role: ov.Role.String(),
			GrantedBy: p.users[ov.GrantedBy].ID, CreatedAt: ts(ov.At)}); err != nil {
			return err
		}
	}
	for _, l := range p.links {
		if err := o.write("share_links.jsonl", ShareLinkRecord{ID: l.ID, NoteID: p.notes[l.Note].ID, Role: l.Role.String(), CreatedBy: p.users[l.CreatedBy].ID,
			CreatedAt: ts(l.CreatedAt), ExpiresAt: tsPtr(l.ExpiresAt), RevokedAt: tsPtr(l.RevokedAt), Token: l.Token, TokenSHA256: jsonl.SHA256Hex([]byte(l.Token))}); err != nil {
			return err
		}
	}
	for _, n := range p.notes {
		for _, t := range n.Tags {
			if err := o.write("tags.jsonl", TagRecord{WorkspaceID: p.ws(n).ID, NoteID: n.ID, Tag: t}); err != nil {
				return err
			}
		}
	}
	return nil
}

// realizeAll runs the note workers and writes their results in note order.
// At most window results are in flight, which bounds memory.
func realizeAll(p *plan, a *aggregate, o *outputs, workers int, logf func(string, ...any)) error {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	window := workers * 4
	jobs := make(chan int)
	type done struct {
		res *noteResult
		err error
	}
	results := make(chan done, window)
	tickets := make(chan struct{}, window)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for k := 0; k < workers; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				res, err := p.realize(idx)
				if err == nil {
					res.idx = idx
				}
				select {
				case results <- done{res, err}:
				case <-stop:
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range p.notes {
			select {
			case tickets <- struct{}{}:
			case <-stop:
				return
			}
			select {
			case jobs <- i:
			case <-stop:
				return
			}
		}
	}()
	pending := map[int]*noteResult{}
	next := 0
	var firstErr error
	progress := max(1, len(p.notes)/10)
	for next < len(p.notes) && firstErr == nil {
		d := <-results
		if d.err != nil {
			firstErr = d.err
			break
		}
		pending[d.res.idx] = d.res
		for {
			res, ok := pending[next]
			if !ok {
				break
			}
			delete(pending, next)
			if err := a.absorb(res, o); err != nil {
				firstErr = err
				break
			}
			<-tickets
			next++
			if next%progress == 0 {
				logf("notes %d/%d", next, len(p.notes))
			}
		}
	}
	close(stop)
	wg.Wait()
	return firstErr
}

// absorb writes one note's records and keeps its summary.
func (a *aggregate) absorb(res *noteResult, o *outputs) error {
	p := a.p
	n := p.notes[res.idx]
	w := p.ws(n)
	write := func(rel string, rec any) error {
		a.counts[rel]++
		return o.write(rel, rec)
	}
	if err := write("notes.jsonl", res.note); err != nil {
		return err
	}
	for _, b := range res.blocks {
		if err := write("blocks.jsonl", b); err != nil {
			return err
		}
	}
	if err := o.writeOps(res.note.OpsPath, res.opsFile); err != nil {
		return err
	}
	for _, b := range res.batches {
		if err := write("batches.jsonl", b); err != nil {
			return err
		}
	}
	for _, b := range res.rejected {
		if err := write("rejected_ops.jsonl", b); err != nil {
			return err
		}
	}
	for _, r := range res.replicas {
		if err := write("replicas.jsonl", r); err != nil {
			return err
		}
	}
	if err := write("truth/crdt/final.jsonl", res.final); err != nil {
		return err
	}
	for _, l := range res.links {
		if err := write("links.jsonl", l); err != nil {
			return err
		}
		if l.TargetNoteID == nil && l.RemovedAt == nil && !n.Trashed {
			a.dangling[w.Index] = append(a.dangling[w.Index], DanglingRecord{WorkspaceID: w.ID, SourceNoteID: n.ID, TargetTitle: l.TargetTitle, BlockID: *l.BlockID})
		}
	}
	for _, c := range res.comments {
		if err := write("comments.jsonl", c); err != nil {
			return err
		}
	}
	for _, nr := range res.notifs {
		if err := write("notifications.jsonl", nr); err != nil {
			return err
		}
	}
	for _, e := range res.embeds {
		if err := write("truth/clustering/block_embeddings.jsonl", e); err != nil {
			return err
		}
	}
	a.targets[res.idx] = res.targets
	a.vecs[res.idx] = res.vec
	for _, h := range res.hits {
		a.qrels[w.Index][h.Query] = append(a.qrels[w.Index][h.Query], Qrel{NoteID: n.ID, Grade: h.Grade})
	}
	for _, m := range res.matches {
		a.matches[w.Index][m.Query] = append(a.matches[w.Index][m.Query], m.Match)
	}
	if res.shingles != nil {
		a.shingles[res.idx] = res.shingles
	}
	if p.keep[res.idx] {
		a.kept[res.idx] = res.finalBlk
	}
	a.counts["ops"] += int64(res.note.OpCount)
	a.counts["rejected_ops"] += int64(len(res.rejected))
	return nil
}

func writeTruth(a *aggregate, o *outputs) error {
	conflicts, err := runConflicts()
	if err != nil {
		return err
	}
	for _, c := range conflicts {
		if err := o.write("truth/crdt/conflicts.jsonl", c); err != nil {
			return err
		}
	}
	if err := a.writeGraphTruth(o); err != nil {
		return err
	}
	if err := a.writeSearchTruth(o); err != nil {
		return err
	}
	if err := a.writeDiscoveryTruth(o); err != nil {
		return err
	}
	if err := a.writeClusteringTruth(o); err != nil {
		return err
	}
	if err := a.writePermissionTruth(o); err != nil {
		return err
	}
	evals, err := a.resolveEvals()
	if err != nil {
		return err
	}
	for _, e := range evals {
		if err := o.write("truth/agent/evals.jsonl", e); err != nil {
			return err
		}
	}
	chats, chatNotifs := a.buildChats(evals)
	for _, c := range chats {
		if err := o.write("chats.jsonl", c); err != nil {
			return err
		}
	}
	for _, nr := range append(a.p.planNotifications(), chatNotifs...) {
		if err := o.write("notifications.jsonl", nr); err != nil {
			return err
		}
	}
	return nil
}

// Manifest is manifest.json.
type Manifest struct {
	FormatVersion    int              `json:"format_version"`
	GeneratorVersion string           `json:"generator_version"`
	Profile          string           `json:"profile"`
	Seed             uint64           `json:"seed"`
	ReferenceTime    string           `json:"reference_time"`
	BlockMarker      string           `json:"block_marker"`
	Embedder         string           `json:"embedder"`
	Counts           map[string]int64 `json:"counts"`
	Files            []jsonl.FileInfo `json:"files"`
	Ops              OpsSummary       `json:"ops"`
}

// OpsSummary describes the ops/ directory without listing every file: each
// note's ops file hash is in notes.jsonl (ops_sha256), and Digest is the
// SHA-256 of the lines "<path> <sha256>\n" for every ops file in note order.
type OpsSummary struct {
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	Digest string `json:"digest"`
}

func manifest(p *plan, a *aggregate, o *outputs) Manifest {
	files := append([]jsonl.FileInfo{}, o.files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	counts := map[string]int64{
		"users": int64(len(p.users)), "workspaces": int64(len(p.workspaces)), "notes": int64(len(p.notes)),
		"ops": a.counts["ops"], "rejected_batches": a.counts["rejected_ops"],
	}
	for _, f := range files {
		if f.Path == "truth/clustering/topics.json" {
			continue
		}
		counts[f.Path] = f.Records
	}
	counts["blocks"] = counts["blocks.jsonl"]
	return Manifest{FormatVersion: FormatVersion, GeneratorVersion: Version, Profile: p.prof.Name, Seed: p.src.Seed(),
		ReferenceTime: ts(ReferenceTime), BlockMarker: "\u001e", Embedder: hashembed.Name, Counts: counts, Files: files,
		Ops: OpsSummary{Files: o.opsN, Bytes: o.opsB, Digest: hex.EncodeToString(o.opsHash.Sum(nil))}}
}
