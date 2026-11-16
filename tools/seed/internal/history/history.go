// Package history builds a note's content the way people build it: through
// edit sessions on several replicas of the real CRDT engine (libs/crdt),
// delivered to a server replica in an order a sync service could see. The
// output is the encoded operation batches, in delivery order, plus the
// server's final text and version vector.
//
// # Model
//
// A Note holds one server Sequence and one Sequence per replica (a device or
// an agent run). A Session is one replica's burst of local edits between two
// syncs:
//
//  1. Opening a session brings the replica up to date with everything the
//     server has applied so far (Sequence.OpsSince). A session that stays
//     open while others are delivered is concurrent with them; one opened
//     long before it is delivered models an offline device.
//  2. Edits are local inserts and deletes on the replica (typing, typo
//     fixes, draft text that is later deleted, block splits and merges). The
//     generated ops are the replica's real output.
//  3. Delivering the session applies its ops to the server in order, which
//     always succeeds because a replica's ops depend only on what it had
//     seen plus its own earlier ops. Rejecting it instead records the batch
//     without applying it (access revoked while offline, viewer edits).
//
// Blocks follow internal/blocks: each starts with a Marker character, and a
// block's ID is its marker's op ID.
//
// # Invariants
//
//   - The server's marker order always equals Note's marker list. To keep
//     that knowable without inspecting the engine's tree, a session that
//     changes block structure (adds, splits or merges blocks) may only be
//     delivered if nothing was delivered since it opened. Concurrent and
//     offline sessions edit text inside existing blocks; Fugue then decides
//     how concurrent runs at the same spot are ordered, and the seed records
//     whatever it decided.
//   - Every delivered op is applied by the server without error, and the
//     server's text and version vector equal a fresh replay of the encoded
//     batches (Replay), with CheckInvariants passing after every batch.
//   - A rejected replica is never used again: its counters were consumed by
//     ops the server never applied, so any later op from it would never be
//     causally ready.
//
// # Cost
//
// The reference engine is O(n) per local edit, so a session costs O(edits ×
// note length). Callers keep edits per session modest; the stress profile
// measures the worst case.
package history

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/tools/seed/internal/blocks"
)

// ServerReplica is the replica ID of the server-side sequence. It never
// generates operations; the value only has to differ from every real
// replica, which are drawn at or above MinReplicaID.
const ServerReplica crdt.ReplicaID = 1

// MinReplicaID is the smallest replica ID handed to devices and agents.
// IDs below it are reserved for the server and for replay checks.
const MinReplicaID crdt.ReplicaID = 1 << 32

// Kind labels what a batch represents, for loaders and test selection.
type Kind string

// Batch kinds.
const (
	KindType       Kind = "type"       // new blocks and text
	KindRevise     Kind = "revise"     // deletes earlier draft text, fixes
	KindSplit      Kind = "split"      // Enter inside a block
	KindMerge      Kind = "merge"      // Backspace at a block start
	KindConcurrent Kind = "concurrent" // edited while another session was open
	KindOffline    Kind = "offline"    // made on a device that synced long before
	KindAgent      Kind = "agent"      // suggestion edits from an agent run
	KindLink       Kind = "link"       // adds or removes [[links]]
)

// Errors returned by this package.
var (
	// ErrStaleStructure means a session that changed block structure was
	// delivered after another delivery, so the marker order could no longer
	// be predicted. This is a bug in the calling script.
	ErrStaleStructure = errors.New("history: structural edit in a stale session")
	// ErrBadEdit means an edit referred to a block or draft that does not
	// exist in the session's view.
	ErrBadEdit = errors.New("history: invalid edit")
	// ErrDeadReplica means a replica whose batch was rejected was used again.
	ErrDeadReplica = errors.New("history: replica was rejected and cannot edit")
	// ErrReplay means replaying the encoded batches did not reproduce the
	// server state.
	ErrReplay = errors.New("history: replay does not match")
)

// Replica describes one editing replica of a note.
type Replica struct {
	ID     crdt.ReplicaID
	UserID string
	// AgentRunID is set when the replica is an agent run acting for UserID.
	AgentRunID string
}

// Batch is one delivered or rejected session.
type Batch struct {
	Seq         int // delivery order, from 0; rejected batches count separately
	Replica     Replica
	Kind        Kind
	CreatedAt   time.Time // when the edits were made
	DeliveredAt time.Time // when the server received them
	Ops         []crdt.Op
	Encoded     []byte // wire format version 1
	Inserts     int
	Deletes     int
	// Reason is set on rejected batches.
	Reason string
}

// Marker is one block's marker: its ID and the block attributes it carries
// (Text unused).
type Marker struct {
	ID     crdt.ID
	Attrs  blocks.Block
	Author Replica
	At     time.Time
}

// Note is one note's editing history in progress.
type Note struct {
	server    *crdt.Sequence
	markers   []Marker
	replicas  map[crdt.ReplicaID]*replicaState
	batches   []Batch
	rejected  []Batch
	delivered int
	// AfterDeliver, when set, is called after every delivered batch with
	// the server's text, so callers can observe how the note changed over
	// time (link history).
	AfterDeliver func(text string, b *Batch)
}

type replicaState struct {
	info Replica
	// seq is nil while the replica has no open session: it is rebuilt
	// from the server's log when the next session opens.
	seq  *crdt.Sequence
	open int
	dead bool
}

// NewNote starts an empty note.
func NewNote() *Note {
	server, err := crdt.New(ServerReplica)
	if err != nil {
		panic(err) // ServerReplica is a non-zero constant
	}
	return &Note{server: server, replicas: map[crdt.ReplicaID]*replicaState{}}
}

// AddReplica registers a replica. Its ID must be at least MinReplicaID and
// unused in this note.
func (n *Note) AddReplica(r Replica) error {
	if r.ID < MinReplicaID {
		return fmt.Errorf("%w: replica %d below the reserved range", ErrBadEdit, r.ID)
	}
	if _, ok := n.replicas[r.ID]; ok {
		return fmt.Errorf("%w: replica %d added twice", ErrBadEdit, r.ID)
	}
	if _, err := crdt.New(r.ID); err != nil {
		return err
	}
	n.replicas[r.ID] = &replicaState{info: r}
	return nil
}

// Delivered returns the number of batches the server has applied.
func (n *Note) Delivered() int { return n.delivered }

// Text returns the server's current text.
func (n *Note) Text() string { return n.server.Text() }

// Markers returns the server's markers in order. The slice is a copy.
func (n *Note) Markers() []Marker { return slices.Clone(n.markers) }

// Batches returns the delivered batches in delivery order.
func (n *Note) Batches() []Batch { return n.batches }

// Rejected returns the rejected batches in the order they were rejected.
func (n *Note) Rejected() []Batch { return n.rejected }

// Version returns the server's version vector.
func (n *Note) Version() crdt.VersionVector { return n.server.Version() }

// OpCount returns the number of operations the server has applied.
func (n *Note) OpCount() uint64 { return n.server.Version().Total() }

// Open starts a session on replica id at time at. The replica first catches
// up with everything the server has applied.
func (n *Note) Open(id crdt.ReplicaID, kind Kind, at time.Time) (*Session, error) {
	rep, ok := n.replicas[id]
	if !ok {
		return nil, fmt.Errorf("%w: unknown replica %d", ErrBadEdit, id)
	}
	if rep.dead {
		return nil, fmt.Errorf("%w: %d", ErrDeadReplica, id)
	}
	if rep.seq == nil {
		seq, err := crdt.New(id)
		if err != nil {
			return nil, err
		}
		rep.seq = seq
	}
	rep.open++
	for _, op := range n.server.OpsSince(rep.seq.Version()) {
		if err := rep.seq.Apply(op); err != nil {
			return nil, fmt.Errorf("catch up replica %d: %w", id, err)
		}
	}
	s := &Session{note: n, rep: rep, kind: kind, createdAt: at, base: n.delivered, markers: slices.Clone(n.markers)}
	if err := s.locateMarkers(); err != nil {
		return nil, err
	}
	return s, nil
}

// release ends a session's hold on its replica. A replica with no open
// session drops its copy of the note: rebuilding it from the server's log on
// the next Open yields the same state (a sequence is a function of the set
// of applied operations), and long notes edited from many devices would
// otherwise keep one full copy per device in memory.
func (s *Session) release() {
	s.rep.open--
	if s.rep.open == 0 {
		s.rep.seq = nil
	}
}

// Session is one replica's local edits between two syncs.
type Session struct {
	note       *Note
	rep        *replicaState
	kind       Kind
	createdAt  time.Time
	base       int
	markers    []Marker
	pos        []int // visible index of each marker in the replica's text
	length     int   // replica's visible length
	ops        []crdt.Op
	structural bool
	closed     bool
}

// locateMarkers finds the marker positions in the replica's text and checks
// they match the expected marker count.
func (s *Session) locateMarkers() error {
	text := s.rep.seq.Text()
	s.pos = s.pos[:0]
	i := 0
	for _, r := range text {
		if r == blocks.Marker {
			s.pos = append(s.pos, i)
		}
		i++
	}
	s.length = i
	if len(s.pos) != len(s.markers) {
		return fmt.Errorf("%w: replica %d sees %d markers, expected %d", ErrBadEdit, s.rep.info.ID, len(s.pos), len(s.markers))
	}
	if len(s.pos) > 0 && s.pos[0] != 0 {
		return fmt.Errorf("%w: text does not start with a marker", ErrBadEdit)
	}
	return nil
}

// Blocks returns the number of blocks in the session's view.
func (s *Session) Blocks() int { return len(s.markers) }

// BlockLen returns the length in code points of block i's text in the
// session's view.
func (s *Session) BlockLen(i int) int { return s.blockEnd(i) - s.pos[i] - 1 }

// Marker returns the session's view of block i's marker.
func (s *Session) Marker(i int) Marker { return s.markers[i] }

// blockEnd returns the visible index just after block i's last character.
func (s *Session) blockEnd(i int) int {
	if i+1 < len(s.pos) {
		return s.pos[i+1]
	}
	return s.length
}

func (s *Session) insert(index int, text string) ([]crdt.Op, error) {
	ops, err := s.rep.seq.Insert(index, text)
	if err != nil {
		return nil, err
	}
	k := utf8.RuneCountInString(text)
	for m := range s.pos {
		if s.pos[m] >= index {
			s.pos[m] += k
		}
	}
	s.length += k
	s.ops = append(s.ops, ops...)
	return ops, nil
}

func (s *Session) delete(index, count int) error {
	for m := range s.pos {
		if s.pos[m] >= index && s.pos[m] < index+count {
			return fmt.Errorf("%w: delete [%d,%d) would remove a marker", ErrBadEdit, index, index+count)
		}
	}
	ops, err := s.rep.seq.Delete(index, count)
	if err != nil {
		return err
	}
	for m := range s.pos {
		if s.pos[m] > index {
			s.pos[m] -= count
		}
	}
	s.length -= count
	s.ops = append(s.ops, ops...)
	return nil
}

func (s *Session) checkOpen() error {
	if s.closed {
		return fmt.Errorf("%w: session already delivered", ErrBadEdit)
	}
	return nil
}

func checkText(text string) error {
	if strings.ContainsRune(text, blocks.Marker) {
		return blocks.ErrMarkerInText
	}
	return nil
}

// AppendBlock adds a new block at the end of the note and types its text,
// fixing the given typos on the way (see Typo). It returns the block's
// marker ID.
func (s *Session) AppendBlock(attrs blocks.Block, text string, typos []Typo) (crdt.ID, error) {
	if err := s.checkOpen(); err != nil {
		return crdt.ID{}, err
	}
	if err := checkText(text); err != nil {
		return crdt.ID{}, err
	}
	ops, err := s.insert(s.length, string(blocks.Marker))
	if err != nil {
		return crdt.ID{}, err
	}
	attrs.Text = ""
	s.markers = append(s.markers, Marker{ID: ops[0].ID, Attrs: attrs, Author: s.rep.info, At: s.createdAt})
	s.pos = append(s.pos, s.length-1)
	s.structural = true
	_, err = s.typeAt(s.length, text, typos)
	return ops[0].ID, err
}

// Index returns the index of the block whose marker has the given ID in the
// session's view.
func (s *Session) Index(marker crdt.ID) (int, error) {
	for i, m := range s.markers {
		if m.ID == marker {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w: no block with marker %s", ErrBadEdit, marker)
}

// Typo is a mistake made while typing and corrected straight away: at code
// point Offset of the typed text, Wrong was typed instead of the intended
// character, then deleted and retyped.
type Typo struct {
	Offset int
	Wrong  rune
}

// typeAt types text starting at index, replaying typos as mistake, delete,
// correction. Typos must be sorted by Offset and inside the text. It returns
// the ID of the first character of text as finally typed (zero for empty
// text), which is never one of the deleted mistakes.
func (s *Session) typeAt(index int, text string, typos []Typo) (crdt.ID, error) {
	runes := []rune(text)
	var first crdt.ID
	chunk := func(from, to int) error {
		ops, err := s.insert(index, string(runes[from:to]))
		if err == nil && from == 0 {
			first = ops[0].ID
		}
		index += to - from
		return err
	}
	done := 0
	for _, t := range typos {
		if t.Offset < done || t.Offset >= len(runes) || t.Wrong == runes[t.Offset] || t.Wrong == blocks.Marker {
			return first, fmt.Errorf("%w: typo at %d in a %d-rune text", ErrBadEdit, t.Offset, len(runes))
		}
		if t.Offset > done {
			if err := chunk(done, t.Offset); err != nil {
				return first, err
			}
		}
		if _, err := s.insert(index, string(t.Wrong)); err != nil {
			return first, err
		}
		if err := s.delete(index, 1); err != nil {
			return first, err
		}
		done = t.Offset
	}
	if done < len(runes) {
		if err := chunk(done, len(runes)); err != nil {
			return first, err
		}
	}
	return first, nil
}

// AppendText types text at the end of block i and returns the ID of its
// first character.
func (s *Session) AppendText(i int, text string, typos []Typo) (crdt.ID, error) {
	if err := s.checkOpen(); err != nil {
		return crdt.ID{}, err
	}
	if i < 0 || i >= len(s.markers) {
		return crdt.ID{}, fmt.Errorf("%w: block %d of %d", ErrBadEdit, i, len(s.markers))
	}
	if err := checkText(text); err != nil {
		return crdt.ID{}, err
	}
	return s.typeAt(s.blockEnd(i), text, typos)
}

// OffsetOf returns the offset within block i's text of the visible character
// with the given ID.
func (s *Session) OffsetOf(i int, id crdt.ID) (int, error) {
	at, visible, ok := s.rep.seq.IndexOf(id)
	if !ok || !visible || i < 0 || i >= len(s.markers) || at <= s.pos[i] || at >= s.blockEnd(i) {
		return 0, fmt.Errorf("%w: character %s is not in block %d", ErrBadEdit, id, i)
	}
	return at - s.pos[i] - 1, nil
}

// Draft is text typed in one session and deleted in a later one. It is
// identified by the IDs of its first and last characters, so it can be found
// again however the text around it changed.
type Draft struct {
	First, Last crdt.ID
	Len         int
}

// InsertDraft types text at the end of block i and returns a handle to
// delete it later.
func (s *Session) InsertDraft(i int, text string) (Draft, error) {
	if err := s.checkOpen(); err != nil {
		return Draft{}, err
	}
	if i < 0 || i >= len(s.markers) || text == "" {
		return Draft{}, fmt.Errorf("%w: draft in block %d", ErrBadEdit, i)
	}
	if err := checkText(text); err != nil {
		return Draft{}, err
	}
	ops, err := s.insert(s.blockEnd(i), text)
	if err != nil {
		return Draft{}, err
	}
	return Draft{First: ops[0].ID, Last: ops[len(ops)-1].ID, Len: len(ops)}, nil
}

// DeleteDraft deletes a draft typed in an earlier (delivered) session. The
// draft must still be contiguous and fully visible in this session's view.
func (s *Session) DeleteDraft(d Draft) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	at, visible, ok := s.rep.seq.IndexOf(d.First)
	if !ok || !visible {
		return fmt.Errorf("%w: draft %s not visible", ErrBadEdit, d.First)
	}
	last, err := s.rep.seq.IDAt(at + d.Len - 1)
	if err != nil || last != d.Last {
		return fmt.Errorf("%w: draft %s is no longer contiguous", ErrBadEdit, d.First)
	}
	return s.delete(at, d.Len)
}

// DeleteRange deletes count code points starting at offset within block i's
// text.
func (s *Session) DeleteRange(i, offset, count int) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if i < 0 || i >= len(s.markers) || offset < 0 || count < 0 || offset+count > s.BlockLen(i) {
		return fmt.Errorf("%w: delete %d at %d in block %d", ErrBadEdit, count, offset, i)
	}
	return s.delete(s.pos[i]+1+offset, count)
}

// InsertText types text at offset within block i's text.
func (s *Session) InsertText(i, offset int, text string) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if i < 0 || i >= len(s.markers) || offset < 0 || offset > s.BlockLen(i) {
		return fmt.Errorf("%w: insert at %d in block %d", ErrBadEdit, offset, i)
	}
	if err := checkText(text); err != nil {
		return err
	}
	_, err := s.insert(s.pos[i]+1+offset, text)
	return err
}

// Split presses Enter at offset within block i's text: the text after it
// becomes a new block i+1 with the given attributes.
func (s *Session) Split(i, offset int, attrs blocks.Block) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if i < 0 || i >= len(s.markers) || offset < 0 || offset > s.BlockLen(i) {
		return fmt.Errorf("%w: split at %d in block %d", ErrBadEdit, offset, i)
	}
	at := s.pos[i] + 1 + offset
	ops, err := s.insert(at, string(blocks.Marker))
	if err != nil {
		return err
	}
	attrs.Text = ""
	s.markers = slices.Insert(s.markers, i+1, Marker{ID: ops[0].ID, Attrs: attrs, Author: s.rep.info, At: s.createdAt})
	s.pos = slices.Insert(s.pos, i+1, at)
	s.structural = true
	return nil
}

// Merge presses Backspace at the start of block i (i > 0): its marker is
// deleted and its text joins block i-1, which keeps its own attributes.
func (s *Session) Merge(i int) error {
	if err := s.checkOpen(); err != nil {
		return err
	}
	if i <= 0 || i >= len(s.markers) {
		return fmt.Errorf("%w: merge block %d of %d", ErrBadEdit, i, len(s.markers))
	}
	at := s.pos[i]
	ops, err := s.rep.seq.Delete(at, 1)
	if err != nil {
		return err
	}
	s.ops = append(s.ops, ops...)
	s.markers = slices.Delete(s.markers, i, i+1)
	s.pos = slices.Delete(s.pos, i, i+1)
	for m := range s.pos {
		if s.pos[m] > at {
			s.pos[m]--
		}
	}
	s.length--
	s.structural = true
	return nil
}

// Empty reports whether the session has produced no operations.
func (s *Session) Empty() bool { return len(s.ops) == 0 }

func (s *Session) batch(at time.Time) Batch {
	b := Batch{Replica: s.rep.info, Kind: s.kind, CreatedAt: s.createdAt, DeliveredAt: at, Ops: s.ops, Encoded: crdt.EncodeOps(s.ops)}
	for _, op := range s.ops {
		if op.Kind == crdt.OpInsert {
			b.Inserts++
		} else {
			b.Deletes++
		}
	}
	return b
}

// Deliver applies the session's ops to the server at time at. An empty
// session delivers nothing and returns false.
func (s *Session) Deliver(at time.Time) (bool, error) {
	if err := s.checkOpen(); err != nil {
		return false, err
	}
	s.closed = true
	defer s.release()
	if len(s.ops) == 0 {
		return false, nil
	}
	n := s.note
	if s.structural && n.delivered != s.base {
		return false, fmt.Errorf("%w: opened after delivery %d, now %d", ErrStaleStructure, s.base, n.delivered)
	}
	for _, op := range s.ops {
		if err := n.server.Apply(op); err != nil {
			return false, fmt.Errorf("server rejected %s: %w", op, err)
		}
	}
	if s.structural {
		n.markers = s.markers
	}
	b := s.batch(at)
	b.Seq = n.delivered
	n.delivered++
	n.batches = append(n.batches, b)
	if n.AfterDeliver != nil {
		n.AfterDeliver(n.server.Text(), &n.batches[len(n.batches)-1])
	}
	return true, nil
}

// Reject records the session's ops as refused by the server, with a reason,
// and retires the replica. An empty session records nothing.
func (s *Session) Reject(at time.Time, reason string) (bool, error) {
	if err := s.checkOpen(); err != nil {
		return false, err
	}
	s.closed = true
	defer s.release()
	if len(s.ops) == 0 {
		return false, nil
	}
	b := s.batch(at)
	b.Seq = len(s.note.rejected)
	b.Reason = reason
	s.note.rejected = append(s.note.rejected, b)
	s.rep.dead = true
	return true, nil
}

// SessionText returns the text as the session's replica sees it now.
func (s *Session) SessionText() string { return s.rep.seq.Text() }

// ReplicaCopies returns how many replicas currently hold a copy of the
// note (have an open session), for memory tests.
func (n *Note) ReplicaCopies() int {
	c := 0
	for _, r := range n.replicas {
		if r.seq != nil {
			c++
		}
	}
	return c
}

// FrameBatches concatenates encoded batches into the per-note ops file
// format: for each batch, its length as a uvarint followed by its bytes. It
// returns the file and the payload offset of each batch.
func FrameBatches(bs []Batch) ([]byte, []int) {
	size := 0
	for _, b := range bs {
		size += len(b.Encoded) + binary.MaxVarintLen64
	}
	out := make([]byte, 0, size)
	offsets := make([]int, len(bs))
	for i, b := range bs {
		out = binary.AppendUvarint(out, uint64(len(b.Encoded)))
		offsets[i] = len(out)
		out = append(out, b.Encoded...)
	}
	return out, offsets
}

// UnframeBatches splits an ops file back into encoded batches.
func UnframeBatches(file []byte) ([][]byte, error) {
	var out [][]byte
	for pos := 0; pos < len(file); {
		n, k := binary.Uvarint(file[pos:])
		if k <= 0 || n > uint64(len(file)-pos-k) {
			return nil, fmt.Errorf("%w: bad frame at byte %d", crdt.ErrCorrupt, pos)
		}
		pos += k
		out = append(out, file[pos:pos+int(n)])
		pos += int(n)
	}
	return out, nil
}

// ReplayResult is the state a fresh replica reaches by applying an ops file.
type ReplayResult struct {
	Text    string
	Version crdt.VersionVector
	Ops     int
}

// Replay decodes an ops file and applies every batch, in order, to a fresh
// sequence, checking the engine's invariants after each batch. Every op must
// be causally ready when its batch is applied.
func Replay(file []byte) (ReplayResult, error) {
	frames, err := UnframeBatches(file)
	if err != nil {
		return ReplayResult{}, err
	}
	seq, err := crdt.New(ServerReplica)
	if err != nil {
		return ReplayResult{}, err
	}
	total := 0
	for i, frame := range frames {
		ops, err := crdt.DecodeOps(frame)
		if err != nil {
			return ReplayResult{}, fmt.Errorf("batch %d: %w", i, err)
		}
		for _, op := range ops {
			if err := seq.Apply(op); err != nil {
				return ReplayResult{}, fmt.Errorf("batch %d: %w", i, err)
			}
		}
		total += len(ops)
		if err := seq.CheckInvariants(); err != nil {
			return ReplayResult{}, fmt.Errorf("batch %d: %w", i, err)
		}
	}
	return ReplayResult{Text: seq.Text(), Version: seq.Version(), Ops: total}, nil
}

// Verify replays the note's encoded batches and checks the result equals the
// server's state.
func (n *Note) Verify() error {
	file, _ := FrameBatches(n.batches)
	got, err := Replay(file)
	if err != nil {
		return err
	}
	if want := n.server.Text(); got.Text != want {
		return fmt.Errorf("%w: text differs (%d vs %d code points)", ErrReplay, utf8.RuneCountInString(got.Text), utf8.RuneCountInString(want))
	}
	if !got.Version.Equal(n.server.Version()) {
		return fmt.Errorf("%w: version vectors differ", ErrReplay)
	}
	return n.server.CheckInvariants()
}

// FinalBlocks splits the server's text into blocks, pairing each with its
// marker's attributes.
func (n *Note) FinalBlocks() ([]Marker, []string, error) {
	prefix, texts := blocks.SplitText(n.server.Text())
	if prefix != "" || len(texts) != len(n.markers) {
		return nil, nil, fmt.Errorf("%w: %d block texts for %d markers", ErrBadEdit, len(texts), len(n.markers))
	}
	return n.Markers(), texts, nil
}
