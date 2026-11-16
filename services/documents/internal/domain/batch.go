package domain

import (
	"fmt"
	"time"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/libs/crdt"
)

// ServerReplica is the replica the documents service uses for operations it
// generates (Markdown import, undo, redo). Devices mint random replica IDs
// at or above 2^32 (ADR-0004, docs/seed-data.md) and replica 1 is the seed's
// replay checker, so 2 never collides; the service refuses client
// operations from it. Only one actor owns a note at a time, so its counters
// for each note stay consecutive.
const ServerReplica crdt.ReplicaID = 2

// BatchKind says why a batch was written. The values are stored in the op
// log and must not change.
type BatchKind uint8

// The batch kinds.
const (
	// KindEdit is a person's edit from a device.
	KindEdit BatchKind = 1
	// KindSuggestion is an agent run's edit (ADR-0009): one ApplyOps call
	// is one change group, so undo rejects the suggestion as a whole.
	KindSuggestion BatchKind = 2
	// KindImport is text the service created (CreateNote's Markdown).
	KindImport BatchKind = 3
	// KindUndo inverts a change group; Ref is the group's first batch.
	KindUndo BatchKind = 4
	// KindRedo inverts an undo batch; Ref is that batch.
	KindRedo BatchKind = 5
	// KindAttrs only writes block attributes (the seed loader).
	KindAttrs BatchKind = 6
)

// String returns the kind's name, as stored in the op log.
func (k BatchKind) String() string {
	switch k {
	case KindEdit:
		return "edit"
	case KindSuggestion:
		return "suggestion"
	case KindImport:
		return "import"
	case KindUndo:
		return "undo"
	case KindRedo:
		return "redo"
	case KindAttrs:
		return "attrs"
	}
	return fmt.Sprintf("BatchKind(%d)", uint8(k))
}

// Batch is one entry of a note's op log.
type Batch struct {
	// Seq numbers the note's batches from 1, in the order applied.
	Seq int64
	// Replica owns every op in the batch; First and Last are their counters
	// (both 0 when the batch has no ops).
	Replica     crdt.ReplicaID
	First, Last uint64
	// For is the replica whose change groups the batch belongs to: the
	// sender for edits and suggestions, the requester for undo and redo,
	// 0 for imports and attribute loads.
	For crdt.ReplicaID
	// Group is the first batch of the ApplyOps call that wrote a
	// suggestion; 0 otherwise.
	Group int64
	// UserID and AgentRunID say whose edit it is (blame).
	UserID     string
	AgentRunID string
	Kind       BatchKind
	// Ref is the batch an undo or redo inverts.
	Ref int64
	At  time.Time

	Ops   []crdt.Op
	Attrs []blocks.AttrWrite
}

// Meta returns the batch without its payload, which is all history needs
// of batches before the latest snapshot.
func (b Batch) Meta() Batch {
	b.Ops, b.Attrs = nil, nil
	return b
}

// Validate checks that the batch's ops belong to Replica with the counters
// First..Last in order.
func (b Batch) Validate() error {
	if len(b.Ops) == 0 {
		if b.First != 0 || b.Last != 0 {
			return fmt.Errorf("%w: batch %d has a counter range but no ops", ErrInvalidArgument, b.Seq)
		}
		return nil
	}
	if b.First == 0 || b.Last-b.First+1 != uint64(len(b.Ops)) {
		return fmt.Errorf("%w: batch %d has %d ops for counters %d..%d", ErrInvalidArgument, b.Seq, len(b.Ops), b.First, b.Last)
	}
	for i, op := range b.Ops {
		if op.ID.Replica != b.Replica || op.ID.Counter != b.First+uint64(i) {
			return fmt.Errorf("%w: batch %d op %d is %s", ErrInvalidArgument, b.Seq, i, op.ID)
		}
	}
	return nil
}

// Sender is who submitted operations for a replica.
type Sender struct {
	Replica    crdt.ReplicaID
	UserID     string
	AgentRunID string
	Kind       BatchKind
	Group      int64
}

// SplitRuns cuts newly applied ops (in the order applied) into batches of
// consecutive ops from one replica, each attributed to that replica's
// sender. Usually there is one run; more appear when a delivery releases
// operations another replica sent earlier out of order. Log order is a
// causal order, so the runs replay in sequence.
func SplitRuns(ops []crdt.Op, senders map[crdt.ReplicaID]Sender, at time.Time) []Batch {
	var out []Batch
	for i := 0; i < len(ops); {
		j := i + 1
		for j < len(ops) && ops[j].ID.Replica == ops[i].ID.Replica {
			j++
		}
		s := senders[ops[i].ID.Replica]
		out = append(out, Batch{
			Replica: ops[i].ID.Replica, First: ops[i].ID.Counter, Last: ops[j-1].ID.Counter,
			For: s.Replica, Group: s.Group, UserID: s.UserID, AgentRunID: s.AgentRunID, Kind: s.Kind,
			At: at, Ops: append([]crdt.Op(nil), ops[i:j]...),
		})
		i = j
	}
	return out
}

// Snapshot is a checkpoint of a note: every op and attribute write applied
// through batch ThroughSeq, in log order. Loading replays it, then the
// batches after it.
type Snapshot struct {
	ThroughSeq int64
	Ops        []crdt.Op
	Attrs      []blocks.AttrWrite
	At         time.Time
}

// Projection is what the service last published about a note's blocks and
// links, so the next BlocksChanged carries only the difference and a crash
// before publishing is repaired on the next load.
type Projection struct {
	// ThroughSeq is the last batch reflected in the published events.
	ThroughSeq int64
	// Digests maps block ID ("replica:counter") to a digest of the block's
	// type, language and text.
	Digests map[string]string
	// Links is the outbound link set, sorted.
	Links []string
}
