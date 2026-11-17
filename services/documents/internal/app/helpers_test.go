package app

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/fakes"
)

func testSeed(t testing.TB) uint64 {
	t.Helper()
	if v := os.Getenv("SKEIN_SEED"); v != "" {
		seed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return seed
	}
	return rand.Uint64()
}

func newRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15)) }

var (
	t0        = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	workspace = domain.WorkspaceID("019c8cf2-1000-769b-b000-73aede463976")
	alice     = domain.Subject{UserID: "alice"}
	bob       = domain.Subject{UserID: "bob"}
	carol     = domain.Subject{UserID: "carol"}
)

// env is a service wired to fakes. Tests may build a second env on the same
// store to simulate a restart.
type env struct {
	svc      *Service
	store    *fakes.Store
	auth     *fakes.Authorizer
	replicas *fakes.Replicas
	clock    *fakes.Clock
	presence *fakes.Presence
}

func newEnv(t *testing.T, cfg Config) *env {
	return newEnvOn(t, fakes.NewStore(), fakes.NewAuthorizer(), fakes.NewReplicas(), fakes.NewClock(t0), cfg)
}

func newEnvOn(t *testing.T, store *fakes.Store, auth *fakes.Authorizer, replicas *fakes.Replicas, clock *fakes.Clock, cfg Config) *env {
	t.Helper()
	if cfg.Tick == 0 {
		cfg.Tick = time.Hour // tests drive periodic work by hand unless they ask
	}
	e := &env{store: store, auth: auth, replicas: replicas, clock: clock, presence: fakes.NewPresence(clock)}
	e.svc = New(Deps{Store: store, Authorizer: auth, Replicas: replicas, Notes: auth, NoteCache: fakes.NewNoteCache(), Presence: e.presence, Clock: clock}, cfg)
	t.Cleanup(e.svc.kill)
	return e
}

// restart simulates a crash: the old service stops without flushing, and a
// new one serves the same store.
func (e *env) restart(t *testing.T, cfg Config) *env {
	e.svc.kill()
	return newEnvOn(t, e.store, e.auth, e.replicas, e.clock, cfg)
}

// newNote creates a note owned by alice, with bob and carol as editors.
func (e *env) newNote(t *testing.T, markdown string) domain.NoteID {
	t.Helper()
	e.auth.AllowCreate(workspace, alice.UserID)
	n, err := e.svc.CreateNote(context.Background(), alice, workspace, "Raft log compaction", markdown)
	if err != nil {
		t.Fatal(err)
	}
	e.auth.SetRole(workspace, n.ID, alice.UserID, fakes.Owner)
	e.auth.SetRole(workspace, n.ID, bob.UserID, fakes.Editor)
	e.auth.SetRole(workspace, n.ID, carol.UserID, fakes.Editor)
	return n.ID
}

// state runs fn on the note's actor.
func (e *env) state(t *testing.T, id domain.NoteID, fn func(*noteState)) {
	t.Helper()
	if err := e.svc.reg.do(context.Background(), id, func(st *noteState) error { fn(st); return nil }); err != nil {
		t.Fatal(err)
	}
}

func (e *env) text(t *testing.T, id domain.NoteID) string {
	var text string
	e.state(t, id, func(st *noteState) {
		if err := st.doc.CheckInvariants(); err != nil {
			t.Fatal(err)
		}
		text = st.doc.Sequence().Text()
	})
	return text
}

func (e *env) version(t *testing.T, id domain.NoteID) crdt.VersionVector {
	var vv crdt.VersionVector
	e.state(t, id, func(st *noteState) { vv = st.doc.Sequence().Version() })
	return vv
}

func eventKinds(events []domain.Event) []domain.EventKind {
	var out []domain.EventKind
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

// client is a simulated device: its own CRDT replica, delivering what the
// server sends through a causal buffer, as the browser's WASM replica does.
type client struct {
	t        *testing.T
	env      *env
	note     domain.NoteID
	subject  domain.Subject
	seq      *crdt.Sequence
	buf      *crdt.CausalBuffer
	sess     *Session
	own      []crdt.Op // every op this client made, for resending after reconnecting
	rejected []Frame
}

func (e *env) client(t *testing.T, note domain.NoteID, subject domain.Subject, replica crdt.ReplicaID) *client {
	seq, err := crdt.New(replica)
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, env: e, note: note, subject: subject, seq: seq, buf: crdt.NewCausalBuffer(seq, 0)}
}

func (c *client) connect() error {
	sess, err := c.env.svc.OpenSession(context.Background(), c.subject, Hello{Note: c.note, Replica: c.seq.Replica(), Version: c.seq.Version()})
	if err != nil {
		return err
	}
	c.sess = sess
	// The client cannot know which of its ops the server has; resending is
	// safe because ops are idempotent.
	if len(c.own) > 0 {
		c.submit(c.own)
	}
	return nil
}

func (c *client) disconnect() {
	if c.sess != nil {
		c.sess.Close(context.Background())
		c.sess = nil
	}
}

func (c *client) submit(ops []crdt.Op) {
	c.t.Helper()
	if err := c.sess.Submit(context.Background(), crdt.EncodeOps(ops)); err != nil {
		c.t.Fatalf("submit: %v", err)
	}
}

// pump delivers every queued frame to the client's replica.
func (c *client) pump() {
	c.t.Helper()
	if c.sess == nil {
		return
	}
	for {
		select {
		case f := <-c.sess.Frames():
			switch f.Kind {
			case FrameOps:
				ops, err := crdt.DecodeOps(f.Ops)
				if err != nil {
					c.t.Fatal(err)
				}
				if err := c.buf.Deliver(ops...); err != nil {
					c.t.Fatalf("client %d: %v", c.seq.Replica(), err)
				}
			case FrameRejected:
				c.rejected = append(c.rejected, f)
			}
		default:
			return
		}
	}
}

func (c *client) insert(index int, text string) []crdt.Op {
	c.t.Helper()
	ops, err := c.seq.Insert(index, text)
	if err != nil {
		c.t.Fatal(err)
	}
	c.own = append(c.own, ops...)
	return ops
}

func (c *client) delete(index, count int) []crdt.Op {
	c.t.Helper()
	ops, err := c.seq.Delete(index, count)
	if err != nil {
		c.t.Fatal(err)
	}
	c.own = append(c.own, ops...)
	return ops
}

func mustErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error %v, want %v", err, want)
	}
}
