package crdt

import (
	"strings"
	"testing"
)

// network simulates an unreliable network between replicas: messages are
// delivered late, in any order, and sometimes twice. Every replica receives
// through a CausalBuffer, as a real client or server does.
type network struct {
	r        interface{ IntN(int) int }
	replicas []*Sequence
	buffers  []*CausalBuffer
	inflight [][]Op // inflight[i] holds ops not yet delivered to replica i
}

func newNetwork(t testing.TB, seed uint64, n int) *network {
	t.Helper()
	net := &network{r: newRand(seed)}
	for i := 0; i < n; i++ {
		s := newSeq(t, ReplicaID(i+1))
		net.replicas = append(net.replicas, s)
		net.buffers = append(net.buffers, NewCausalBuffer(s, 0))
		net.inflight = append(net.inflight, nil)
	}
	return net
}

// broadcast queues ops from replica `from` for every other replica.
func (n *network) broadcast(from int, ops []Op) {
	for i := range n.replicas {
		if i != from {
			n.inflight[i] = append(n.inflight[i], ops...)
		}
	}
}

// deliverSome delivers a random subset of one replica's in-flight ops in a
// random order, occasionally duplicating one.
func (n *network) deliverSome(t testing.TB, to int) {
	t.Helper()
	q := n.inflight[to]
	if len(q) == 0 {
		return
	}
	k := 1 + n.r.IntN(len(q))
	batch := make([]Op, 0, k+1)
	for j := 0; j < k; j++ {
		i := n.r.IntN(len(q))
		batch = append(batch, q[i])
		q[i] = q[len(q)-1]
		q = q[:len(q)-1]
	}
	if n.r.IntN(4) == 0 {
		batch = append(batch, batch[n.r.IntN(len(batch))])
	}
	n.inflight[to] = q
	if err := n.buffers[to].Deliver(batch...); err != nil {
		t.Fatalf("replica %d: Deliver: %v", to+1, err)
	}
}

func (n *network) deliverAll(t testing.TB) {
	t.Helper()
	for to := range n.replicas {
		for len(n.inflight[to]) > 0 {
			n.deliverSome(t, to)
		}
		if p := n.buffers[to].Pending(); p != 0 {
			t.Fatalf("replica %d still has %d pending ops after full delivery", to+1, p)
		}
	}
}

// Strong eventual consistency: replicas that have applied the same ops show
// the same text, whatever the order and duplication of delivery.
func TestConvergenceUnderReorderingAndDuplication(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	for round := 0; round < 40; round++ {
		replicas := 2 + r.IntN(4)
		net := newNetwork(t, r.Uint64(), replicas)
		steps := 20 + r.IntN(120)
		for step := 0; step < steps; step++ {
			i := r.IntN(replicas)
			if r.IntN(3) == 0 {
				net.deliverSome(t, i)
			} else {
				net.broadcast(i, randomEdit(t, r, net.replicas[i]))
			}
			if err := net.replicas[i].CheckInvariants(); err != nil {
				t.Fatalf("round %d step %d: %v", round, step, err)
			}
		}
		net.deliverAll(t)
		want := net.replicas[0].Text()
		for _, s := range net.replicas {
			mustValid(t, s)
			if s.Text() != want {
				t.Fatalf("round %d: replica %d has %q, replica 1 has %q", round, s.Replica(), s.Text(), want)
			}
			if !s.Version().Equal(net.replicas[0].Version()) {
				t.Fatalf("round %d: version vectors differ", round)
			}
		}
	}
}

// Replaying a replica's log into a fresh replica must reproduce it exactly.
// This is what loading a document from the op log does.
func TestReplayingTheLogReproducesTheText(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	net := newNetwork(t, r.Uint64(), 3)
	for step := 0; step < 300; step++ {
		i := r.IntN(3)
		net.broadcast(i, randomEdit(t, r, net.replicas[i]))
		if r.IntN(2) == 0 {
			net.deliverSome(t, r.IntN(3))
		}
	}
	for _, s := range net.replicas {
		fresh := newSeq(t, 99)
		mustApply(t, fresh, s.OpsSince(nil))
		if fresh.Text() != s.Text() {
			t.Fatalf("replay of replica %d gives %q, want %q", s.Replica(), fresh.Text(), s.Text())
		}
	}
}

// Fugue's main guarantee: text typed concurrently at the same position by
// different replicas is not interleaved. Each replica's run stays contiguous.
func TestConcurrentTypingAtSamePositionDoesNotInterleave(t *testing.T) {
	seed := testSeed(t)
	t.Logf("seed %d", seed)
	r := newRand(seed)
	for round := 0; round < 200; round++ {
		base := newSeq(t, 100)
		baseOps := mustInsert(t, base, 0, randomText(r, 6))
		pos := r.IntN(base.Len() + 1)

		n := 2 + r.IntN(3)
		replicas := make([]*Sequence, n)
		runs := make([]string, n)
		for i := range replicas {
			replicas[i] = newSeq(t, ReplicaID(i+1))
			mustApply(t, replicas[i], baseOps)
			// Type the run one character at a time, like a person does.
			runs[i] = strings.Repeat(string(rune('A'+i)), 2+r.IntN(5))
			for k := range []rune(runs[i]) {
				mustInsert(t, replicas[i], pos+k, runs[i][k:k+1])
			}
		}
		syncAll(t, replicas...)
		merged := replicas[0].Text()
		for i, run := range runs {
			if !strings.Contains(merged, run) {
				t.Fatalf("round %d: run %q of replica %d interleaved in %q", round, run, i+1, merged)
			}
		}
	}
}

// Backward typing (typing at the start of a line, or pressing Home and
// typing) must not interleave either.
func TestConcurrentBackwardTypingDoesNotInterleave(t *testing.T) {
	a, b := newSeq(t, 1), newSeq(t, 2)
	for _, c := range "abc" {
		mustInsert(t, a, 0, string(c))
	}
	for _, c := range "xyz" {
		mustInsert(t, b, 0, string(c))
	}
	syncAll(t, a, b)
	if got := a.Text(); got != "cbazyx" {
		t.Fatalf("merged = %q, want %q", got, "cbazyx")
	}
	if a.Text() != b.Text() {
		t.Fatalf("replicas diverged: %q vs %q", a.Text(), b.Text())
	}
}
