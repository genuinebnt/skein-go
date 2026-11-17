//go:build e2e

package e2e

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/libs/crdt"
)

// replica is one editing device: a CRDT sequence kept in step with the
// note's sync socket. Local edits apply at once and are sent when online;
// remote operations arrive on the socket and apply under the same lock.
type replica struct {
	t      testing.TB
	u      *user
	noteID string
	id     uint64

	mu       sync.Mutex
	seq      *crdt.Sequence
	pending  []crdt.Op // remote ops not causally ready yet
	rejected []*skeinv1.SyncRejected
	conn     *websocket.Conn
	done     chan struct{} // closed when the reader stops
	closeErr error         // why the reader stopped
	frames   int           // ops frames received, for waiting
	changed  chan struct{} // signalled on every applied remote batch
}

// newReplica registers a fresh replica ID for u and starts it empty.
func newReplica(t testing.TB, u *user, noteID string) *replica {
	t.Helper()
	id := newReplicaID(t)
	u.call(http.MethodPost, "/v1/replicas", &skeinv1.RegisterReplicaRequest{ReplicaId: id}, nil)
	seq, err := crdt.New(crdt.ReplicaID(id))
	if err != nil {
		t.Fatal(err)
	}
	return &replica{t: t, u: u, noteID: noteID, id: id, seq: seq, changed: make(chan struct{}, 1)}
}

func versionProto(v crdt.VersionVector) *skeinv1.VersionVector {
	out := &skeinv1.VersionVector{}
	for _, r := range v.Replicas() {
		out.Entries = append(out.Entries, &skeinv1.VersionEntry{ReplicaId: uint64(r), Counter: v.Get(r)})
	}
	return out
}

// connect opens the sync socket, says hello with the local version and
// resends this replica's own operations (idempotent on the server), so
// edits made offline reach it.
func (r *replica) connect() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := r.u.dial(ctx, "/v1/notes/"+r.noteID+"/sync")
	if err != nil {
		return err
	}
	r.mu.Lock()
	hello := &skeinv1.SyncRequest{Message: &skeinv1.SyncRequest_Hello{Hello: &skeinv1.SyncHello{
		NoteId: r.noteID, ReplicaId: r.id, Version: versionProto(r.seq.Version()),
	}}}
	var own []crdt.Op
	for _, op := range r.seq.OpsSince(crdt.VersionVector{}) {
		if uint64(op.ID.Replica) == r.id {
			own = append(own, op)
		}
	}
	r.conn, r.done, r.closeErr = conn, make(chan struct{}), nil
	r.mu.Unlock()
	if err := writeProto(ctx, conn, hello); err != nil {
		return err
	}
	if len(own) > 0 {
		if err := writeProto(ctx, conn, &skeinv1.SyncRequest{Message: &skeinv1.SyncRequest_Ops{Ops: crdt.EncodeOps(own)}}); err != nil {
			return err
		}
	}
	go r.read(conn, r.done)
	return nil
}

func (r *replica) mustConnect() *replica {
	r.t.Helper()
	if err := r.connect(); err != nil {
		r.t.Fatalf("replica %d: %v", r.id, err)
	}
	return r
}

func (r *replica) read(conn *websocket.Conn, done chan struct{}) {
	defer close(done)
	for {
		var resp skeinv1.SyncResponse
		err := readProto(context.Background(), conn, &resp)
		if err != nil {
			r.mu.Lock()
			r.closeErr = err
			r.mu.Unlock()
			return
		}
		switch m := resp.GetMessage().(type) {
		case *skeinv1.SyncResponse_Ops:
			ops, err := crdt.DecodeOps(m.Ops)
			if err != nil {
				r.t.Errorf("replica %d: undecodable ops frame: %v", r.id, err)
				continue
			}
			r.mu.Lock()
			r.pending = append(r.pending, ops...)
			r.drainPending()
			r.frames++
			r.mu.Unlock()
			select {
			case r.changed <- struct{}{}:
			default:
			}
		case *skeinv1.SyncResponse_Rejected:
			r.mu.Lock()
			r.rejected = append(r.rejected, m.Rejected)
			r.mu.Unlock()
		}
	}
}

// drainPending applies every pending op that has become causally ready.
// The server sends causally ordered batches, so this normally applies all
// of them at once; holding back is only a safety net.
func (r *replica) drainPending() {
	for progress := true; progress && len(r.pending) > 0; {
		progress = false
		rest := r.pending[:0]
		for _, op := range r.pending {
			err := r.seq.Apply(op)
			switch {
			case err == nil:
				progress = true
			case errors.Is(err, crdt.ErrNotReady):
				rest = append(rest, op)
			default:
				r.t.Errorf("replica %d: applying %s: %v", r.id, op, err)
			}
		}
		r.pending = rest
	}
	if err := r.seq.CheckInvariants(); err != nil {
		r.t.Errorf("replica %d: %v", r.id, err)
	}
}

// disconnect closes the socket normally and waits for the reader.
func (r *replica) disconnect() {
	r.mu.Lock()
	conn, done := r.conn, r.done
	r.conn = nil
	r.mu.Unlock()
	if conn == nil {
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "going offline")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = conn.CloseNow()
	}
}

// insert types text at index and sends it if online.
func (r *replica) insert(index int, text string) {
	r.t.Helper()
	r.mu.Lock()
	ops, err := r.seq.Insert(index, text)
	conn := r.conn
	r.mu.Unlock()
	if err != nil {
		r.t.Fatalf("replica %d: insert: %v", r.id, err)
	}
	r.send(conn, ops)
}

// appendText types text at the end.
func (r *replica) appendText(text string) { r.insert(r.length(), text) }

// remove deletes count characters at index and sends it if online.
func (r *replica) remove(index, count int) {
	r.t.Helper()
	r.mu.Lock()
	ops, err := r.seq.Delete(index, count)
	conn := r.conn
	r.mu.Unlock()
	if err != nil {
		r.t.Fatalf("replica %d: delete: %v", r.id, err)
	}
	r.send(conn, ops)
}

func (r *replica) send(conn *websocket.Conn, ops []crdt.Op) {
	r.t.Helper()
	if conn == nil || len(ops) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := writeProto(ctx, conn, &skeinv1.SyncRequest{Message: &skeinv1.SyncRequest_Ops{Ops: crdt.EncodeOps(ops)}}); err != nil {
		r.mu.Lock()
		why := r.closeErr
		r.mu.Unlock()
		r.t.Fatalf("replica %d: send: %v (socket ended with: %v)", r.id, err, why)
	}
}

func (r *replica) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq.Text()
}

func (r *replica) length() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq.Len()
}

func (r *replica) version() crdt.VersionVector {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seq.Version()
}

func (r *replica) rejections() []*skeinv1.SyncRejected {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*skeinv1.SyncRejected(nil), r.rejected...)
}

// waitClosed waits for the server to close the socket and returns the close
// code (-1 when it ended without a close frame).
func (r *replica) waitClosed(timeout time.Duration) (websocket.StatusCode, string) {
	r.t.Helper()
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	select {
	case <-done:
	case <-time.After(timeout):
		r.t.Fatalf("replica %d: socket still open after %s", r.id, timeout)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var ce websocket.CloseError
	if errors.As(r.closeErr, &ce) {
		return ce.Code, ce.Reason
	}
	return -1, ""
}

// converge waits until every replica has the same version and text, and
// returns that text.
func converge(t testing.TB, timeout time.Duration, replicas ...*replica) string {
	t.Helper()
	var text string
	eventually(t, timeout, "replicas converge", func() (bool, string) {
		first := replicas[0]
		v, s := first.version(), first.text()
		state := ""
		for _, r := range replicas[1:] {
			if !r.version().Equal(v) || r.text() != s {
				state += "replica " + strconv.FormatUint(r.id, 10) + " has " + strconv.Quote(r.text()) + " vs " + strconv.Quote(s) + "; "
			}
		}
		text = s
		return state == "", state
	})
	return text
}
