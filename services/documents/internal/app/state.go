package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// noteState is one open note, owned by its actor goroutine. Nothing here is
// safe for concurrent use; the actor serialises every call.
type noteState struct {
	svc    *Service
	note   domain.Note
	doc    *blocks.Document
	buffer *crdt.CausalBuffer
	// batches holds the metadata of every batch in the op log, in order.
	batches []domain.Batch
	senders map[crdt.ReplicaID]domain.Sender

	lastSnapshotSeq  int64
	opsSinceSnapshot int

	projection domain.Projection
	// dirtySince is when the first change not yet in the projection was
	// made; zero when the projection is current.
	dirtySince time.Time
	lastActor  domain.Subject

	sessions map[*Session]struct{}
	lastUsed time.Time

	cachedBlocks    []blocks.Block
	cachedBlocksKey [2]uint64
}

func (st *noteState) lastSeq() int64 {
	if len(st.batches) == 0 {
		return 0
	}
	return st.batches[len(st.batches)-1].Seq
}

// loadNoteState rebuilds a note from its latest snapshot and the batches
// after it. Anything that fails to replay means the stored log is corrupt;
// the note is refused rather than served wrong.
func loadNoteState(ctx context.Context, svc *Service, id domain.NoteID) (*noteState, error) {
	note, err := svc.store.GetNote(ctx, id)
	if err != nil {
		return nil, err
	}
	doc, err := blocks.NewDocument(domain.ServerReplica)
	if err != nil {
		return nil, err
	}
	snap, haveSnap, err := svc.store.LatestSnapshot(ctx, id)
	if err != nil {
		return nil, err
	}
	corrupt := func(what string, err error) error {
		return fmt.Errorf("%w: note %s: %s: %v", domain.ErrUnavailable, id, what, err)
	}
	if haveSnap {
		for _, op := range snap.Ops {
			if err := doc.Sequence().Apply(op); err != nil {
				return nil, corrupt("snapshot", err)
			}
		}
		for _, w := range snap.Attrs {
			if _, err := doc.Attributes().Apply(w); err != nil {
				return nil, corrupt("snapshot attributes", err)
			}
		}
	}
	batches, err := svc.store.LoadBatches(ctx, id, snap.ThroughSeq)
	if err != nil {
		return nil, err
	}
	st := &noteState{
		svc: svc, note: note, doc: doc,
		buffer:          crdt.NewCausalBuffer(doc.Sequence(), svc.cfg.MaxPending),
		senders:         map[crdt.ReplicaID]domain.Sender{},
		lastSnapshotSeq: snap.ThroughSeq,
		sessions:        map[*Session]struct{}{},
		lastUsed:        svc.clock.Now(),
	}
	for _, b := range batches {
		if b.Seq > snap.ThroughSeq {
			if err := st.replay(b); err != nil {
				return nil, corrupt(fmt.Sprintf("batch %d", b.Seq), err)
			}
			st.opsSinceSnapshot += len(b.Ops)
		}
		st.batches = append(st.batches, b.Meta())
	}
	if st.projection, err = svc.store.LoadProjection(ctx, id); err != nil {
		return nil, err
	}
	if st.projection.ThroughSeq < st.lastSeq() {
		// A crash came between a commit and its events: publish now.
		st.dirtySince = time.Unix(0, 0).UTC()
	}
	return st, nil
}

func (st *noteState) replay(b domain.Batch) error {
	for _, op := range b.Ops {
		if err := st.doc.Sequence().Apply(op); err != nil {
			return err
		}
	}
	for _, w := range b.Attrs {
		if _, err := st.doc.Attributes().Apply(w); err != nil {
			return err
		}
	}
	return nil
}

// blocks returns the current layout, recomputed only after a change. The
// number of ops and attribute writes only grows, so it identifies the state.
func (st *noteState) blocks() ([]blocks.Block, error) {
	key := [2]uint64{st.doc.Sequence().Version().Total(), uint64(st.doc.Attributes().Len())}
	if st.cachedBlocks != nil && key == st.cachedBlocksKey {
		return st.cachedBlocks, nil
	}
	bs, err := st.doc.Blocks()
	if err != nil {
		return nil, err
	}
	if bs == nil {
		bs = []blocks.Block{}
	}
	st.cachedBlocks, st.cachedBlocksKey = bs, key
	return bs, nil
}

// deliverResult is what happened to a submitted batch.
type deliverResult struct {
	Rejected []crdt.Op
	Reason   string
}

// deliver applies operations from one sender, persists what became applied,
// and only then sends it to the other sessions: nothing is acknowledged or
// broadcast before it is durable. Operations that cannot be applied (reused
// IDs, a full buffer) are returned as rejected; operations not yet causally
// ready wait in the buffer and are applied when their dependencies arrive.
func (st *noteState) deliver(ctx context.Context, sender domain.Sender, subject domain.Subject, ops []crdt.Op) (deliverResult, error) {
	if err := st.note.CheckEditable(); err != nil {
		return deliverResult{}, err
	}
	now := st.svc.clock.Now()
	st.lastUsed = now
	st.senders[sender.Replica] = sender
	seq := st.doc.Sequence()
	before := seq.Version()
	var res deliverResult
	for _, op := range ops {
		if err := st.buffer.Deliver(op); err != nil {
			res.Rejected = append(res.Rejected, op)
			res.Reason = rejectReason(err)
		}
	}
	batches := domain.SplitRuns(seq.OpsSince(before), st.senders, now)
	// One ApplyOps call from an agent is one suggestion group, named by its
	// first batch, so undo rejects the suggestion as a whole.
	group := int64(0)
	for i := range batches {
		b := &batches[i]
		b.Seq = st.lastSeq() + int64(i) + 1
		if b.Replica == sender.Replica && sender.Kind == domain.KindSuggestion {
			if group == 0 {
				group = b.Seq
			}
			b.Group = group
		}
	}
	if group != 0 {
		sender.Group = group
		st.senders[sender.Replica] = sender
	}
	if err := st.commit(ctx, batches, subject, now); err != nil {
		return res, err
	}
	return res, nil
}

// commit persists batches already applied in memory, records them and
// fans them out. A failed append poisons the actor.
func (st *noteState) commit(ctx context.Context, batches []domain.Batch, subject domain.Subject, now time.Time) error {
	if len(batches) == 0 {
		return nil
	}
	if err := st.svc.store.AppendBatches(ctx, st.note.ID, batches, now); err != nil {
		return poisoned{err}
	}
	for _, b := range batches {
		st.batches = append(st.batches, b.Meta())
		st.opsSinceSnapshot += len(b.Ops)
		st.svc.m.applied.Add(ctx, int64(len(b.Ops)), metric.WithAttributes(attribute.String("skein.batch.kind", b.Kind.String())))
	}
	st.note.UpdatedAt = now
	st.lastActor = subject
	if st.dirtySince.IsZero() {
		st.dirtySince = now
	}
	st.fanOut(batches)
	if st.opsSinceSnapshot >= st.svc.cfg.SnapshotEvery {
		st.snapshot(ctx, now)
	}
	return nil
}

func rejectReason(err error) string {
	switch {
	case errors.Is(err, crdt.ErrIDConflict):
		return "operation id reused with different content; mint a new replica"
	case errors.Is(err, crdt.ErrBufferFull):
		return "too many operations waiting for missing ones; resync"
	default:
		return "invalid operation"
	}
}

// fanOut sends each batch's ops to every session except the one whose
// replica wrote them (it has them already).
func (st *noteState) fanOut(batches []domain.Batch) {
	for _, b := range batches {
		if len(b.Ops) == 0 {
			continue
		}
		frame := Frame{Kind: FrameOps, Ops: crdt.EncodeOps(b.Ops)}
		for s := range st.sessions {
			if s.replica != b.Replica {
				st.send(s, frame)
			}
		}
	}
}

// send queues a frame without blocking the actor. A session that cannot
// keep up is closed; its client reconnects and catches up from its
// version vector.
func (st *noteState) send(s *Session, f Frame) {
	if !s.offer(f) {
		st.dropSession(s, fmt.Errorf("%w: sync client fell behind; reconnect", domain.ErrResourceExhausted))
	}
}

func (st *noteState) dropSession(s *Session, err error) {
	delete(st.sessions, s)
	s.close(err)
	st.lastUsed = st.svc.clock.Now()
}

func (st *noteState) closeSessions(err error) {
	for s := range st.sessions {
		st.dropSession(s, err)
	}
}

// snapshot checkpoints the note. A failure is logged and retried at the
// next threshold: the op log alone still loads the note.
func (st *noteState) snapshot(ctx context.Context, now time.Time) {
	if st.lastSeq() == st.lastSnapshotSeq {
		return
	}
	snap := domain.Snapshot{
		ThroughSeq: st.lastSeq(),
		Ops:        st.doc.Sequence().OpsSince(crdt.VersionVector{}),
		Attrs:      st.doc.Attributes().Writes(),
		At:         now,
	}
	if err := st.svc.store.SaveSnapshot(ctx, st.note.ID, snap); err != nil {
		st.svc.logger.Warn("snapshot failed", "note_id", string(st.note.ID), "error", err)
		return
	}
	st.lastSnapshotSeq, st.opsSinceSnapshot = snap.ThroughSeq, 0
}

// flushProjection publishes BlocksChanged and LinksChanged for changes made
// at least the debounce interval ago (or now, when forced).
func (st *noteState) flushProjection(ctx context.Context, force bool) error {
	if st.dirtySince.IsZero() {
		return nil
	}
	now := st.svc.clock.Now()
	if !force && now.Sub(st.dirtySince) < st.svc.cfg.Debounce {
		return nil
	}
	bs, err := st.blocks()
	if err != nil {
		return err
	}
	next, events := domain.NextProjection(st.note, st.projection, bs, st.lastSeq(), st.lastActor, now)
	if _, err := st.svc.store.PublishProjection(ctx, st.note.ID, next, events); err != nil {
		return err
	}
	st.projection, st.dirtySince = next, time.Time{}
	return nil
}

// tick runs periodic work and reports whether the note is idle and fully
// flushed, so the actor may stop.
func (st *noteState) tick(ctx context.Context) bool {
	if err := st.flushProjection(ctx, false); err != nil {
		st.svc.logger.Warn("publishing block changes failed", "note_id", string(st.note.ID), "error", err)
	}
	if len(st.sessions) > 0 || st.svc.clock.Now().Sub(st.lastUsed) < st.svc.cfg.IdleTimeout {
		return false
	}
	if err := st.flushProjection(ctx, true); err != nil {
		return false
	}
	st.snapshot(ctx, st.svc.clock.Now())
	return true
}

// stop flushes and closes every session; it runs when the actor stops.
func (st *noteState) stop(ctx context.Context, closeErr error) {
	if err := st.flushProjection(ctx, true); err != nil {
		st.svc.logger.Warn("publishing block changes on stop failed", "note_id", string(st.note.ID), "error", err)
	}
	st.snapshot(ctx, st.svc.clock.Now())
	if closeErr == nil {
		closeErr = fmt.Errorf("%w: note closed", domain.ErrUnavailable)
	}
	st.closeSessions(closeErr)
}

// invert undoes (or redoes) the given batches' operations as server ops
// for replica, persists them as one batch of kind, and returns their
// encoding.
func (st *noteState) invert(ctx context.Context, subject domain.Subject, replica crdt.ReplicaID, seqs []int64, kind domain.BatchKind, ref int64) ([]byte, error) {
	if err := st.note.CheckEditable(); err != nil {
		return nil, err
	}
	var ops []crdt.Op
	for _, seq := range seqs {
		b, err := st.svc.store.LoadBatch(ctx, st.note.ID, seq)
		if err != nil {
			return nil, err
		}
		ops = append(ops, b.Ops...)
	}
	now := st.svc.clock.Now()
	st.lastUsed = now
	before := st.doc.Sequence().Version().Get(domain.ServerReplica)
	inverse, writes, err := domain.Inverse(st.doc, ops)
	if err != nil {
		return nil, poisoned{err}
	}
	batch := domain.Batch{
		Seq: st.lastSeq() + 1, Replica: domain.ServerReplica, For: replica, Kind: kind, Ref: ref,
		UserID: subject.UserID, AgentRunID: subject.AgentRunID, At: now, Ops: inverse, Attrs: writes,
	}
	if len(inverse) > 0 {
		batch.First, batch.Last = before+1, inverse[len(inverse)-1].ID.Counter
	}
	if err := st.commit(ctx, []domain.Batch{batch}, subject, now); err != nil {
		return nil, err
	}
	return crdt.EncodeOps(inverse), nil
}
