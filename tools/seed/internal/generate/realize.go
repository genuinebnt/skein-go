package generate

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/tools/seed/internal/blocks"
	"github.com/genuinebnt/skein-go/tools/seed/internal/hashembed"
	"github.com/genuinebnt/skein-go/tools/seed/internal/history"
	"github.com/genuinebnt/skein-go/tools/seed/internal/jsonl"
	"github.com/genuinebnt/skein-go/tools/seed/internal/policy"
	"github.com/genuinebnt/skein-go/tools/seed/internal/prng"
)

// noteResult is everything one note contributes to the output, produced by
// a worker and written by the single writer in note order.
type noteResult struct {
	idx      int
	note     NoteRecord
	blocks   []BlockRecord
	opsFile  []byte
	batches  []BatchRecord
	rejected []RejectedRecord
	final    FinalRecord
	links    []LinkRecord
	targets  []int
	comments []CommentRecord
	notifs   []NotificationRecord
	replicas []ReplicaRecord
	embeds   []EmbeddingRecord
	vec      [hashembed.Dim]float32
	hits     []queryHit
	matches  []regexHit
	shingles []uint64
	finalBlk []finalBlock
}

type finalBlock struct {
	ID    string
	Block blocks.Block
	Topic int
}

type queryHit struct {
	Query int
	Grade int
}

type regexHit struct {
	Query int
	Match regexMatch
}

// EmbeddingRecord is a line of truth/clustering/block_embeddings.jsonl.
type EmbeddingRecord struct {
	BlockID     string                 `json:"block_id"`
	NoteID      string                 `json:"note_id"`
	WorkspaceID string                 `json:"workspace_id"`
	Topic       *string                `json:"topic"`
	Vector      [hashembed.Dim]float64 `json:"vector"`
}

var linkPattern = regexp.MustCompile(`\[\[([^\[\]\n]+)\]\]`)

// linkTitles returns the distinct [[link]] titles in the non-code blocks,
// in order of first appearance, with the index of the block holding each.
func linkTitles(bs []blocks.Block) ([]string, map[string]int) {
	var out []string
	where := map[string]int{}
	for i, b := range bs {
		if b.Type == blocks.Code {
			continue
		}
		for _, m := range linkPattern.FindAllStringSubmatch(b.Text, -1) {
			if _, seen := where[m[1]]; !seen {
				where[m[1]] = i
				out = append(out, m[1])
			}
		}
	}
	return out, where
}

// script drives one note's edit history.
type script struct {
	p       *plan
	n       *notePlan
	r       *prng.Rand
	hn      *history.Note
	gb      []genBlock
	markers []crdt.ID // marker of each planned block, once created
	extra   []crdt.ID // the second marker of a block typed in two halves
	hosted  []bool    // block typed inside the previous block, split later
	split   []crdt.ID // first character of a hosted block
	t       time.Time
	exact   bool
	runs    []agentRun
	pending []pendingReject
	// end is the latest time a session may be delivered.
	end time.Time
	// extraReplicas are replicas created for this note only (stress).
	extraReplicas []ReplicaRecord
	// busy holds replicas with a session that is still open (concurrent,
	// offline or about to be rejected); no other session may use them.
	busy map[crdt.ReplicaID]bool
}

type agentRun struct {
	ID      string
	UserIdx int
	Replica crdt.ReplicaID
	At      time.Time
}

// task is a deferred edit: done in a later session.
type task struct {
	structural bool
	block      int // planned block it touches, -1 if none
	kind       history.Kind
	do         func(s *history.Session) error
}

func (sc *script) advance() time.Time {
	var gap time.Duration
	switch x := sc.r.Float64(); {
	case x < 0.7:
		gap = time.Duration(5+sc.r.IntN(85)) * time.Minute
	case x < 0.95:
		gap = time.Duration(1+sc.r.IntN(47)) * time.Hour
	default:
		gap = time.Duration(3+sc.r.IntN(11)) * 24 * time.Hour
	}
	// Approach the end without passing it, so late sessions stay inside
	// the note's editable lifetime.
	if left := sc.end.Sub(sc.t); gap > left/4 {
		gap = max(0, left/4)
	}
	sc.t = sc.t.Add(gap)
	return sc.t
}

func (sc *script) canEdit(user int, at time.Time) bool {
	return sc.p.policy.Evaluate(policy.Subject{UserID: sc.p.users[user].ID}, policy.NoteEdit, sc.n.ID, at).Allowed
}

func (sc *script) freeDevices(user int) []crdt.ReplicaID {
	var out []crdt.ReplicaID
	for _, d := range sc.p.users[user].Devices {
		if !sc.busy[d.Replica] {
			out = append(out, d.Replica)
		}
	}
	return out
}

// author picks an editor allowed to edit throughout [from, to] who has a
// free device. If none of the note's editors qualifies (all removed or
// busy), the workspace owner, who can always edit, takes over; -1 means
// nobody can.
func (sc *script) author(from, to time.Time) int {
	var ok []int
	eligible := func(u int) bool {
		return sc.canEdit(u, from) && sc.canEdit(u, to) && len(sc.freeDevices(u)) > 0
	}
	for _, u := range sc.n.Editors {
		if eligible(u) {
			ok = append(ok, u)
		}
	}
	if len(ok) == 0 {
		if owner := sc.p.ws(sc.n).Owner; eligible(owner) {
			sc.ensureReplicas(owner)
			return owner
		}
		return -1
	}
	return ok[sc.r.IntN(len(ok))]
}

// ensureReplicas registers a user's devices with the note if needed.
func (sc *script) ensureReplicas(user int) {
	for _, d := range sc.p.users[user].Devices {
		_ = sc.hn.AddReplica(history.Replica{ID: d.Replica, UserID: sc.p.users[user].ID}) // already added is fine
	}
}

// open starts a session on a free device of user.
func (sc *script) open(user int, kind history.Kind, at time.Time) (*history.Session, crdt.ReplicaID, error) {
	free := sc.freeDevices(user)
	if len(free) == 0 {
		return nil, 0, fmt.Errorf("user %d has no free device", user)
	}
	rep := free[sc.r.IntN(len(free))]
	s, err := sc.hn.Open(rep, kind, at)
	return s, rep, err
}

func (sc *script) deliver(s *history.Session, at time.Time) error {
	if at.After(sc.end) {
		at = sc.end
	}
	_, err := s.Deliver(at)
	return err
}

// single runs one task in its own session. It reports false, changing
// nothing, when no editor is free to do it now; the caller retries later.
func (sc *script) single(t task) (bool, error) {
	at := sc.advance()
	u := sc.author(at, at)
	if u < 0 {
		return false, nil
	}
	s, _, err := sc.open(u, t.kind, at)
	if err != nil {
		return false, err
	}
	if err := t.do(s); err != nil {
		return false, err
	}
	return true, sc.deliver(s, at.Add(time.Duration(1+sc.r.IntN(20))*time.Minute))
}

func (sc *script) index(s *history.Session, block int) (int, error) {
	return s.Index(sc.markers[block])
}

// typos plans a couple of corrected typos in text.
func (sc *script) typos(text string) []history.Typo {
	rs := []rune(text)
	var out []history.Typo
	for i := 1; i < len(rs); i++ {
		if sc.r.Chance(0.006) && rs[i] >= 'a' && rs[i] <= 'z' {
			wrong := rune('a' + (rs[i]-'a'+1+rune(sc.r.IntN(24)))%26)
			if wrong != rs[i] {
				out = append(out, history.Typo{Offset: i, Wrong: wrong})
			}
		}
	}
	return out
}

func isProse(t blocks.Type) bool {
	return t == blocks.Paragraph || t == blocks.Bulleted || t == blocks.Numbered || t == blocks.Quote
}

// cut returns a word boundary near the middle of text, or -1.
func cut(text string) int {
	rs := []rune(text)
	if len(rs) < 40 {
		return -1
	}
	for i := len(rs) / 2; i < len(rs)-5; i++ {
		if rs[i] == ' ' {
			return i
		}
	}
	return -1
}

// run produces a generated or anchor note's history. The creator types the
// first group of blocks; later sessions type the remaining groups and do the
// deferred work decided up front: continuations of blocks, deleting drafts,
// splitting blocks that were typed as one, merging blocks typed as two, the
// agent's suggestion, and draft links. Loose (non-structural) work is also
// done in concurrent pairs and offline sessions. A task nobody may do at the
// moment is retried later, never dropped, so the final text always holds
// the planned content.
func (sc *script) run() error {
	gb := sc.gb
	m := len(gb)
	sc.markers = make([]crdt.ID, m)
	sc.extra = make([]crdt.ID, m)
	sc.hosted = make([]bool, m)
	sc.split = make([]crdt.ID, m)
	agentBlock := -1
	if m > 1 && gb[m-1].Agent {
		agentBlock = m - 1
	}
	typedEnd := m
	if agentBlock >= 0 {
		typedEnd = m - 1
	}
	sessions := sc.p.prof.SessionsMin + sc.r.IntN(sc.p.prof.SessionsMax-sc.p.prof.SessionsMin+1)
	if sc.n.Anchor != nil {
		sessions = 3 + sc.r.IntN(4)
	}
	if typedEnd == 0 {
		typedEnd = m
		agentBlock = -1
	}
	groups := max(1, min(sessions/2, typedEnd))
	bounds := []int{0}
	firstEnd := max(1, typedEnd*(4+sc.r.IntN(4))/10)
	if groups == 1 {
		firstEnd = typedEnd
	}
	bounds = append(bounds, firstEnd)
	for g := 1; g < groups; g++ {
		bounds = append(bounds, firstEnd+(typedEnd-firstEnd)*g/(groups-1))
	}
	bounds = uniqueInts(bounds)
	groupOf := make([]int, m)
	for g := 0; g+1 < len(bounds); g++ {
		for b := bounds[g]; b < bounds[g+1]; b++ {
			groupOf[b] = g
		}
	}
	// Decide deferred work per block.
	cont := make([]string, m) // text appended in a later session
	halves := make([]int, m)  // rune offset where a block is typed as two halves
	allowExtras := sessions >= 3
	for b := 0; b < typedEnd; b++ {
		blk := gb[b].Block
		switch {
		case !allowExtras || !isProse(blk.Type):
		case b > 0 && b < typedEnd && groupOf[b] == groupOf[b-1] && isProse(gb[b-1].Type) && gb[b-1].Text != "" && blk.Text != "" &&
			!sc.hosted[b-1] && halves[b-1] == 0 && cont[b-1] == "" && sc.r.Chance(0.06):
			sc.hosted[b] = true
		case sc.r.Chance(0.04) && blk.Type == blocks.Paragraph && cut(blk.Text) > 0:
			halves[b] = cut(blk.Text)
		case sc.r.Chance(0.15) && cut(blk.Text) > 0:
			c := cut(blk.Text)
			cont[b] = string([]rune(blk.Text)[c:])
		}
	}
	// A block that hosts the next one must not also be split in halves or
	// continued later, or the split offset would move.
	for b := 1; b < m; b++ {
		if sc.hosted[b] {
			halves[b-1], cont[b-1] = 0, ""
			halves[b], cont[b] = 0, ""
		}
	}
	var structural, loose []task
	var drafts []history.Draft
	typeGroup := func(s *history.Session, g int) error {
		for b := bounds[g]; b < bounds[g+1]; b++ {
			blk := gb[b].Block
			text := blk.Text
			if cont[b] != "" {
				text = string([]rune(text)[:len([]rune(text))-utf8.RuneCountInString(cont[b])])
			}
			if sc.hosted[b] {
				i, err := sc.index(s, b-1)
				if err != nil {
					return err
				}
				id, err := s.AppendText(i, text, sc.typos(text))
				if err != nil {
					return err
				}
				sc.split[b] = id
				attrs := blk
				bb := b
				structural = append(structural, task{structural: true, block: b, kind: history.KindSplit, do: func(s *history.Session) error {
					i, err := sc.index(s, bb-1)
					if err != nil {
						return err
					}
					off, err := s.OffsetOf(i, sc.split[bb])
					if err != nil {
						return err
					}
					if err := s.Split(i, off, attrs); err != nil {
						return err
					}
					sc.markers[bb] = s.Marker(i + 1).ID
					return nil
				}})
				continue
			}
			if halves[b] > 0 {
				rs := []rune(text)
				id, err := s.AppendBlock(blk, string(rs[:halves[b]]), nil)
				if err != nil {
					return err
				}
				sc.markers[b] = id
				second, err := s.AppendBlock(blocks.Block{Type: blocks.Paragraph}, string(rs[halves[b]:]), nil)
				if err != nil {
					return err
				}
				sc.extra[b] = second
				bb := b
				structural = append(structural, task{structural: true, block: b, kind: history.KindMerge, do: func(s *history.Session) error {
					i, err := s.Index(sc.extra[bb])
					if err != nil {
						return err
					}
					return s.Merge(i)
				}})
				continue
			}
			id, err := s.AppendBlock(blk, text, sc.typos(text))
			if err != nil {
				return err
			}
			sc.markers[b] = id
			if cont[b] != "" {
				bb, rest := b, cont[b]
				kind := history.KindType
				if strings.Contains(rest, "[[") {
					kind = history.KindLink
				}
				loose = append(loose, task{block: b, kind: kind, do: func(s *history.Session) error {
					i, err := sc.index(s, bb)
					if err != nil {
						return err
					}
					_, err = s.AppendText(i, rest, nil)
					return err
				}})
			}
		}
		return nil
	}
	// draft adds text to a typed prose block that a later session deletes.
	draft := func(s *history.Session, text string, kind history.Kind) error {
		var cands []int
		for b := 0; b < typedEnd; b++ {
			if sc.markers[b] != (crdt.ID{}) && isProse(gb[b].Type) && sc.extra[b] == (crdt.ID{}) && (b+1 >= m || !sc.hosted[b+1]) {
				cands = append(cands, b)
			}
		}
		if len(cands) == 0 {
			return nil
		}
		b := cands[sc.r.IntN(len(cands))]
		i, err := sc.index(s, b)
		if err != nil {
			return err
		}
		d, err := s.InsertDraft(i, text)
		if err != nil {
			return err
		}
		drafts = append(drafts, d)
		loose = append(loose, task{block: -1, kind: kind, do: func(s *history.Session) error { return s.DeleteDraft(d) }})
		return nil
	}
	// Session 1: the creator types the first group.
	sc.t = sc.n.CreatedAt
	if !sc.canEdit(sc.n.CreatedBy, sc.t) {
		return fmt.Errorf("creator cannot edit the note when creating it")
	}
	s, _, err := sc.open(sc.n.CreatedBy, history.KindType, sc.t)
	if err != nil {
		return err
	}
	if err := typeGroup(s, 0); err != nil {
		return err
	}
	if sc.r.Chance(0.25) && sc.n.Anchor == nil {
		if err := draft(s, " "+capitalize(prng.Pick(sc.r, []string{"maybe", "todo", "check"}))+": "+lowerFirst(snippet(sc.gb[0].Text, 40))+"?", history.KindRevise); err != nil {
			return err
		}
	}
	sc.t = sc.t.Add(time.Duration(2+sc.r.IntN(30)) * time.Minute)
	if err := sc.deliver(s, sc.t); err != nil {
		return err
	}
	// Overrides start a minute after creation; check rejections an hour on.
	firstDone := sc.t.Add(time.Hour)
	if err := sc.rejections(firstDone); err != nil {
		return err
	}
	for _, t := range sc.n.DraftLinks {
		title := sc.p.notes[t].Title
		loose = append(loose, task{block: -1, kind: history.KindLink, do: func(s *history.Session) error {
			return draft(s, fmt.Sprintf(" See also [[%s]].", title), history.KindLink)
		}})
	}
	nextGroup := 1
	agentDone := agentBlock < 0
	remaining := func() bool {
		return nextGroup < len(bounds)-1 || len(structural) > 0 || len(loose) > 0 || !agentDone
	}
	takeLoose := func(avoid int) (task, bool) {
		for i, t := range loose {
			if avoid < 0 || t.block < 0 || t.block != avoid {
				loose = append(loose[:i], loose[i+1:]...)
				return t, true
			}
		}
		return task{}, false
	}
	for guard := 0; remaining(); guard++ {
		if guard > 10*m+50 {
			return fmt.Errorf("note %d: history did not finish", sc.n.Index)
		}
		x := sc.r.Float64()
		switch {
		case nextGroup < len(bounds)-1 && (x < 0.45 || len(loose) == 0 && len(structural) == 0):
			g := nextGroup
			ok, err := sc.single(task{structural: true, kind: history.KindType, do: func(s *history.Session) error { return typeGroup(s, g) }})
			if err != nil {
				return err
			}
			if ok {
				nextGroup++
			}
		case len(structural) > 0 && x < 0.6:
			ok, err := sc.single(structural[0])
			if err != nil {
				return err
			}
			if ok {
				structural = structural[1:]
			}
		case len(loose) >= 2 && x < 0.72:
			if err := sc.concurrentPair(&loose, takeLoose); err != nil {
				return err
			}
		case len(loose) >= 1 && x < 0.82:
			if err := sc.offline(&loose, takeLoose, nextGroup < len(bounds)-1 || len(structural) > 0 || !agentDone); err != nil {
				return err
			}
		case len(loose) > 0:
			t, _ := takeLoose(-1)
			ok, err := sc.single(t)
			if err != nil {
				return err
			}
			if !ok {
				loose = append(loose, t)
			}
		case !agentDone && nextGroup >= len(bounds)-1 && len(structural) == 0:
			ok, err := sc.agentSession(agentBlock)
			if err != nil {
				return err
			}
			agentDone = ok
		case len(structural) > 0:
			ok, err := sc.single(structural[0])
			if err != nil {
				return err
			}
			if ok {
				structural = structural[1:]
			}
		}
	}
	return sc.finishRejections()
}

// concurrentPair runs two loose tasks from two sessions opened on the same
// base and delivered one after the other.
func (sc *script) concurrentPair(loose *[]task, take func(int) (task, bool)) error {
	a, _ := take(-1)
	avoid := -1
	if sc.exact {
		avoid = a.block
	}
	b, ok := take(avoid)
	if !ok {
		if done, err := sc.single(a); err != nil || done {
			return err
		}
		*loose = append(*loose, a)
		return nil
	}
	at := sc.advance()
	ua := sc.author(at, at)
	if ua < 0 {
		*loose = append(*loose, a, b)
		return nil
	}
	sa, ra, err := sc.open(ua, history.KindConcurrent, at)
	if err != nil {
		return err
	}
	sc.busy[ra] = true
	ub := sc.author(at, at)
	if ub < 0 {
		// Nobody else can edit at the same time: run them one after another.
		delete(sc.busy, ra)
		if err := a.do(sa); err != nil {
			return err
		}
		if err := sc.deliver(sa, at.Add(2*time.Minute)); err != nil {
			return err
		}
		*loose = append(*loose, b)
		return nil
	}
	sb, _, err := sc.open(ub, history.KindConcurrent, at)
	delete(sc.busy, ra)
	if err != nil {
		return err
	}
	if err := a.do(sa); err != nil {
		return err
	}
	if err := b.do(sb); err != nil {
		return err
	}
	if err := sc.deliver(sa, at.Add(time.Duration(1+sc.r.IntN(5))*time.Minute)); err != nil {
		return err
	}
	return sc.deliver(sb, at.Add(time.Duration(6+sc.r.IntN(5))*time.Minute))
}

// offline opens a session, lets up to two other sessions be delivered, and
// only then makes and delivers its edits from its stale view. Notes that
// must end exactly as planned skip it while structural work is pending,
// because text typed from a stale view at the end of the last block would
// land after blocks appended meanwhile.
func (sc *script) offline(loose *[]task, take func(int) (task, bool), structuralPending bool) error {
	t, _ := take(-1)
	requeue := func(ok bool, err error) error {
		if err == nil && !ok {
			*loose = append(*loose, t)
		}
		return err
	}
	if sc.exact && structuralPending {
		return requeue(sc.single(t))
	}
	opened := sc.advance()
	back := opened.Add(time.Duration(6+sc.r.IntN(60)) * time.Hour)
	if back.After(sc.end) {
		back = opened.Add(sc.end.Sub(opened) / 2)
	}
	u := sc.author(opened, back)
	if u < 0 {
		return requeue(sc.single(t))
	}
	off, rep, err := sc.open(u, history.KindOffline, opened)
	if err != nil {
		return err
	}
	sc.busy[rep] = true
	avoid := -1
	if sc.exact {
		avoid = t.block
	}
	for k := 0; k < 2; k++ {
		o, ok := take(avoid)
		if !ok {
			break
		}
		done, err := sc.single(o)
		if err != nil {
			return err
		}
		if !done {
			*loose = append(*loose, o)
		}
	}
	delete(sc.busy, rep)
	if err := t.do(off); err != nil {
		return err
	}
	if back.Before(sc.t) {
		back = sc.t.Add(time.Minute)
	}
	sc.t = back
	return sc.deliver(off, back)
}

// agentSession types the agent's suggested block from an agent run acting
// for an editor.
func (sc *script) agentSession(block int) (bool, error) {
	at := sc.advance()
	u := sc.author(at, at)
	if u < 0 {
		return false, nil
	}
	rr := sc.p.src.Stream("note.agent", uint64(sc.n.Index))
	run := agentRun{UserIdx: u, At: at}
	run.ID = sc.p.newID("agent-run", sc.n.Index, at)
	for run.Replica < history.MinReplicaID {
		run.Replica = crdt.ReplicaID(rr.Uint64())
	}
	if err := sc.hn.AddReplica(history.Replica{ID: run.Replica, UserID: sc.p.users[u].ID, AgentRunID: run.ID}); err != nil {
		return false, err
	}
	s, err := sc.hn.Open(run.Replica, history.KindAgent, at)
	if err != nil {
		return false, err
	}
	id, err := s.AppendBlock(sc.gb[block].Block, sc.gb[block].Text, nil)
	if err != nil {
		return false, err
	}
	sc.markers[block] = id
	sc.runs = append(sc.runs, run)
	return true, sc.deliver(s, at.Add(2*time.Minute))
}

// pendingReject is an edit that will be refused when delivered.
type pendingReject struct {
	s      *history.Session
	at     time.Time
	reason string
}

func (sc *script) rejections(after time.Time) error {
	add := func(user int, reason string, made, delivered time.Time) error {
		if delivered.After(sc.end) {
			return nil
		}
		free := sc.freeDevices(user)
		if len(free) == 0 {
			return nil
		}
		s, err := sc.hn.Open(free[0], history.KindOffline, made)
		if err != nil {
			return err
		}
		// The device stays offline until it is refused; nothing else may
		// use it in this note.
		sc.busy[free[0]] = true
		var b int
		for b = 0; b < len(sc.markers) && sc.markers[b] == (crdt.ID{}); b++ {
		}
		if b == len(sc.markers) {
			return nil
		}
		i, err := sc.index(s, b)
		if err != nil {
			return err
		}
		if _, err := s.AppendText(i, " (Offline edit: "+lowerFirst(prng.Pick(sc.r, []string{"double-check this", "this is out of date", "add the numbers from last week"}))+")", nil); err != nil {
			return err
		}
		sc.pending = append(sc.pending, pendingReject{s: s, at: delivered, reason: reason})
		return nil
	}
	if u := sc.n.RejectRevoked; u >= 0 {
		removedAt := sc.p.policy.RemovedAt(sc.p.ws(sc.n).ID, sc.p.users[u].ID)
		if removedAt != nil && removedAt.After(after.Add(3*time.Hour)) && sc.canEdit(u, after) {
			made := between(sc.r, after.Add(time.Hour), removedAt.Add(-time.Hour))
			if err := add(u, "access_revoked", made, removedAt.Add(time.Duration(1+sc.r.IntN(72))*time.Hour)); err != nil {
				return err
			}
		}
	}
	if u := sc.n.RejectViewer; u >= 0 && !sc.canEdit(u, after) {
		if err := add(u, "insufficient_role", after.Add(10*time.Minute), after.Add(time.Duration(20+sc.r.IntN(600))*time.Minute)); err != nil {
			return err
		}
	}
	return nil
}

func (sc *script) finishRejections() error {
	for _, pr := range sc.pending {
		if _, err := pr.s.Reject(pr.at, pr.reason); err != nil {
			return err
		}
	}
	return nil
}

// realize generates one note completely.
func (p *plan) realize(idx int) (*noteResult, error) {
	n := p.notes[idx]
	w := p.ws(n)
	gb := p.content(n)
	hn := history.NewNote()
	sc := &script{p: p, n: n, r: p.src.Stream("note.history", uint64(idx)), hn: hn, gb: gb, exact: n.Anchor != nil, busy: map[crdt.ReplicaID]bool{}, end: ReferenceTime.Add(-time.Hour)}
	if n.Trashed {
		sc.end = n.TrashedAt.Add(-time.Hour)
	}
	users := append([]int{}, n.Editors...)
	if n.RejectRevoked >= 0 {
		users = append(users, n.RejectRevoked)
	}
	if n.RejectViewer >= 0 {
		users = append(users, n.RejectViewer)
	}
	for _, u := range uniqueInts(users) {
		for _, d := range p.users[u].Devices {
			if err := hn.AddReplica(history.Replica{ID: d.Replica, UserID: p.users[u].ID}); err != nil {
				return nil, err
			}
		}
	}
	tracker := newLinkTracker()
	hn.AfterDeliver = func(text string, b *history.Batch) {
		_, texts := blocks.SplitText(text)
		ms := hn.Markers()
		bs := make([]blocks.Block, len(texts))
		for i := range texts {
			if i < len(ms) {
				bs[i] = ms[i].Attrs
			}
			bs[i].Text = texts[i]
		}
		titles, _ := linkTitles(bs)
		tracker.observe(titles, b.DeliveredAt)
	}
	var err error
	if n.Stress != "" {
		err = p.stressHistory(sc)
	} else {
		err = sc.run()
	}
	if err != nil {
		return nil, fmt.Errorf("note %d (%s): %w", idx, n.Title, err)
	}
	if err := hn.Verify(); err != nil {
		return nil, fmt.Errorf("note %d (%s): %w", idx, n.Title, err)
	}
	markers, texts, err := hn.FinalBlocks()
	if err != nil {
		return nil, fmt.Errorf("note %d: %w", idx, err)
	}
	res := &noteResult{idx: idx}
	topicOf := map[crdt.ID]int{}
	for b, id := range sc.markers {
		if id != (crdt.ID{}) {
			topicOf[id] = gb[b].Topic
		}
	}
	final := make([]blocks.Block, len(markers))
	for i, mk := range markers {
		b := mk.Attrs
		b.Text = texts[i]
		final[i] = b
		t, ok := topicOf[mk.ID]
		if !ok {
			t = n.Topic
		}
		res.finalBlk = append(res.finalBlk, finalBlock{ID: blockID(mk.ID), Block: b, Topic: t})
		rec := BlockRecord{NoteID: n.ID, BlockID: blockID(mk.ID), Position: i, Type: string(b.Type), Indent: b.Indent, Text: b.Text,
			AuthorID: mk.Author.UserID, CreatedAt: ts(mk.At)}
		if b.Type == blocks.Todo {
			c := b.Checked
			rec.Checked = &c
		}
		if b.Type == blocks.Code {
			rec.Language = strPtr(b.Language)
		}
		res.blocks = append(res.blocks, rec)
	}
	if n.Anchor != nil || n.Stress == "" {
		if err := checkPlannedText(n, gb, final, sc.exact); err != nil {
			return nil, fmt.Errorf("note %d (%s): %w", idx, n.Title, err)
		}
	}
	text := hn.Text()
	// Ops file and batch records.
	res.opsFile, _ = history.FrameBatches(hn.Batches())
	_, offsets := history.FrameBatches(hn.Batches())
	inserts, deletes := 0, 0
	replicaSet := map[crdt.ReplicaID]bool{}
	var updated time.Time
	for i, b := range hn.Batches() {
		inserts += b.Inserts
		deletes += b.Deletes
		replicaSet[b.Replica.ID] = true
		if b.DeliveredAt.After(updated) {
			updated = b.DeliveredAt
		}
		res.batches = append(res.batches, BatchRecord{NoteID: n.ID, Seq: b.Seq, ReplicaID: replicaString(b.Replica.ID), UserID: b.Replica.UserID,
			AgentRunID: strPtr(b.Replica.AgentRunID), Kind: string(b.Kind), CreatedAt: ts(b.CreatedAt), DeliveredAt: ts(b.DeliveredAt),
			OpCount: len(b.Ops), Inserts: b.Inserts, Deletes: b.Deletes, ByteOffset: offsets[i], ByteLength: len(b.Encoded), SHA256: jsonl.SHA256Hex(b.Encoded)})
	}
	for _, b := range hn.Rejected() {
		res.rejected = append(res.rejected, RejectedRecord{NoteID: n.ID, Seq: b.Seq, ReplicaID: replicaString(b.Replica.ID), UserID: b.Replica.UserID,
			Kind: string(b.Kind), CreatedAt: ts(b.CreatedAt), DeliveredAt: ts(b.DeliveredAt), Reason: b.Reason, OpCount: len(b.Ops),
			OpsBase64: base64.StdEncoding.EncodeToString(b.Encoded), SHA256: jsonl.SHA256Hex(b.Encoded)})
	}
	res.replicas = append(res.replicas, sc.extraReplicas...)
	for _, run := range sc.runs {
		res.replicas = append(res.replicas, ReplicaRecord{ReplicaID: replicaString(run.Replica), UserID: p.users[run.UserIdx].ID, Kind: "agent",
			AgentRunID: strPtr(run.ID), RegisteredAt: ts(run.At)})
		res.notifs = append(res.notifs, NotificationRecord{ID: p.newID("notif-agent", idx*8+len(res.notifs), run.At.Add(3*time.Minute)), UserID: p.users[run.UserIdx].ID,
			Kind: "NOTIFICATION_KIND_AGENT_RUN_FINISHED", Title: "Suggested edits ready", Body: "The assistant added a summary to " + n.Title + ".",
			ActorID: p.users[run.UserIdx].ID, CreatedAt: ts(run.At.Add(3 * time.Minute)), Read: run.At.Before(ReferenceTime.Add(-7 * 24 * time.Hour)),
			Location: &Location{NoteID: n.ID, BlockID: res.blocks[len(res.blocks)-1].BlockID}, Source: SourceRef{Kind: "agent_run", ID: run.ID}})
	}
	if updated.IsZero() {
		updated = n.CreatedAt
	}
	textSum := jsonl.SHA256Hex([]byte(text))
	opsSum := jsonl.SHA256Hex(res.opsFile)
	length := utf8.RuneCountInString(text)
	res.note = NoteRecord{ID: n.ID, WorkspaceID: w.ID, Title: n.Title, Kind: n.Kind, Language: languageOf(n), CreatedBy: p.users[n.CreatedBy].ID,
		CreatedAt: ts(n.CreatedAt), UpdatedAt: ts(updated), Trashed: n.Trashed, IsRoot: n.Root, BlockCount: len(final), CharCount: length - len(final),
		OpCount: hn.OpCount(), Batches: len(hn.Batches()), OpsPath: opsPath(idx, n.ID), OpsSHA256: opsSum, TextSHA256: textSum}
	if n.DupOf >= 0 {
		res.note.DuplicateOf = &p.notes[n.DupOf].ID
	}
	if n.Trashed {
		res.note.TrashedAt = tsPtr(&n.TrashedAt)
	}
	res.final = FinalRecord{NoteID: n.ID, WorkspaceID: w.ID, Text: text, TextSHA256: textSum, Length: length, Blocks: len(final),
		VersionVector: versionEntries(hn.Version()), Ops: hn.OpCount(), Inserts: inserts, Deletes: deletes, Tombstones: inserts - length,
		Batches: len(hn.Batches()), Replicas: len(replicaSet)}
	// Links: final state and history.
	titles, where := linkTitles(final)
	for _, iv := range tracker.intervals() {
		rec := LinkRecord{WorkspaceID: w.ID, SourceNoteID: n.ID, TargetTitle: iv.title, AddedAt: ts(iv.added), RemovedAt: tsPtr(iv.removed)}
		if t, ok := w.TitleIndex[iv.title]; ok {
			if t < 0 {
				return nil, fmt.Errorf("note %d links to ambiguous title %q", idx, iv.title)
			}
			rec.TargetNoteID = &p.notes[t].ID
		}
		if iv.removed == nil {
			bi, ok := where[iv.title]
			if !ok {
				return nil, fmt.Errorf("note %d: link %q tracked but not in final text", idx, iv.title)
			}
			rec.BlockID = &res.blocks[bi].BlockID
		}
		res.links = append(res.links, rec)
	}
	for _, t := range titles {
		if ti, ok := w.TitleIndex[t]; ok && ti >= 0 && !p.notes[ti].Trashed && !n.Trashed {
			res.targets = append(res.targets, ti)
		}
	}
	res.targets = uniqueInts(res.targets)
	if !n.Trashed {
		p.scanSearch(n, final, res)
	}
	p.embedNote(n, res)
	if p.needShingles[idx] {
		res.shingles = shingles(final)
	}
	p.comments(n, res)
	return res, nil
}

func languageOf(n *notePlan) string {
	if n.Lang != "" {
		return n.Lang
	}
	return "en"
}

func opsPath(idx int, id string) string {
	return fmt.Sprintf("ops/%04d/%s.ops", idx/1000, id)
}

// checkPlannedText verifies the final text has the planned content: exactly
// (anchor notes, which have no concurrent edits at the same spot) or as the
// same multiset of planned link titles (generated notes, where Fugue may
// order concurrent runs either way).
func checkPlannedText(n *notePlan, gb []genBlock, final []blocks.Block, exact bool) error {
	planned := make([]blocks.Block, len(gb))
	for i, g := range gb {
		planned[i] = g.Block
	}
	if exact {
		if len(planned) != len(final) {
			return fmt.Errorf("final text has %d blocks, planned %d", len(final), len(planned))
		}
		for i := range planned {
			if planned[i] != final[i] {
				return fmt.Errorf("block %d differs from the plan: %+v vs %+v", i, final[i], planned[i])
			}
		}
		return nil
	}
	if len(planned) != len(final) {
		return fmt.Errorf("final text has %d blocks, planned %d", len(final), len(planned))
	}
	want, _ := linkTitles(planned)
	got, _ := linkTitles(final)
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(want, "\x00") != strings.Join(got, "\x00") {
		return fmt.Errorf("final links %q differ from planned %q", got, want)
	}
	return nil
}

// linkTracker records when each [[link]] title appears and disappears.
type linkTracker struct {
	open   map[string]time.Time
	closed []linkInterval
}

type linkInterval struct {
	title   string
	added   time.Time
	removed *time.Time
}

func newLinkTracker() *linkTracker { return &linkTracker{open: map[string]time.Time{}} }

func (lt *linkTracker) observe(titles []string, at time.Time) {
	now := map[string]bool{}
	for _, t := range titles {
		now[t] = true
		if _, ok := lt.open[t]; !ok {
			lt.open[t] = at
		}
	}
	for _, t := range sortedKeys(lt.open) {
		if !now[t] {
			removed := at
			lt.closed = append(lt.closed, linkInterval{title: t, added: lt.open[t], removed: &removed})
			delete(lt.open, t)
		}
	}
}

// intervals returns every interval, closed ones first in closing order, then
// open ones by title.
func (lt *linkTracker) intervals() []linkInterval {
	out := append([]linkInterval{}, lt.closed...)
	for _, t := range sortedKeys(lt.open) {
		out = append(out, linkInterval{title: t, added: lt.open[t]})
	}
	return out
}

// scanSearch grades the note against its workspace's text queries and runs
// its regex queries.
func (p *plan) scanSearch(n *notePlan, final []blocks.Block, res *noteResult) {
	w := p.ws(n)
	title := newTermIndex(n.Title)
	var all strings.Builder
	all.WriteString(n.Title)
	idx := make([]termIndex, len(final))
	for i, b := range final {
		idx[i] = newTermIndex(b.Text)
		all.WriteString("\n")
		all.WriteString(b.Text)
	}
	whole := newTermIndex(all.String())
	for qi := range w.Queries {
		q := &w.Queries[qi]
		if q.Subject >= 0 && !p.policy.Evaluate(policy.Subject{UserID: p.users[q.Subject].ID}, policy.NoteRead, n.ID, ReferenceTime).Allowed {
			continue
		}
		if g := gradeNote(q, title, idx, whole); g > 0 {
			res.hits = append(res.hits, queryHit{Query: qi, Grade: g})
		}
	}
	for qi := range w.Regexes {
		q := &w.Regexes[qi]
		for bi, b := range final {
			rs := []rune(b.Text)
			for _, m := range matchRegex(q, b) {
				res.matches = append(res.matches, regexHit{Query: qi, Match: regexMatch{NoteID: n.ID, BlockID: res.blocks[bi].BlockID,
					Start: m[0], End: m[1], Text: string(rs[m[0]:m[1]])}})
			}
		}
	}
}

func (p *plan) embedNote(n *notePlan, res *noteResult) {
	var all strings.Builder
	all.WriteString(n.Title)
	for _, b := range res.finalBlk {
		all.WriteString("\n")
		all.WriteString(b.Block.Text)
	}
	v := hashembed.Embed(all.String())
	for i := range v {
		res.vec[i] = float32(v[i])
	}
	if n.Trashed || n.Index%p.prof.EmbedEveryNth != 0 {
		return
	}
	for _, b := range res.finalBlk {
		if strings.TrimSpace(b.Block.Text) == "" {
			continue
		}
		rec := EmbeddingRecord{BlockID: b.ID, NoteID: n.ID, WorkspaceID: p.ws(n).ID, Vector: hashembed.Embed(b.Block.Text)}
		if b.Topic >= 0 {
			rec.Topic = &topicIDs[b.Topic]
		}
		res.embeds = append(res.embeds, rec)
	}
}

// shingles returns the sorted set of hashed word 3-grams of the note's text,
// for exact Jaccard similarity of near-duplicate pairs.
func shingles(final []blocks.Block) []uint64 {
	var words []string
	for _, b := range final {
		words = append(words, searchTokens(b.Text)...)
	}
	set := map[uint64]bool{}
	for i := 0; i+3 <= len(words); i++ {
		set[fnv64(words[i]+" "+words[i+1]+" "+words[i+2])] = true
	}
	out := make([]uint64, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func fnv64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
