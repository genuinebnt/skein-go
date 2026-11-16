package seed

import (
	"context"
	"fmt"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/ports"
)

// Replicas is a ports.ReplicaRegistry over replicas.jsonl: identity's own
// loader registers these, and the documents loader must agree with it
// without calling identity.
type Replicas struct {
	owners map[crdt.ReplicaID]string
}

var _ ports.ReplicaRegistry = (*Replicas)(nil)

// NewReplicas reads replicas.jsonl.
func NewReplicas(d *Dataset) (*Replicas, error) {
	recs, err := readJSONL[replicaRecord](d, "replicas.jsonl")
	if err != nil {
		return nil, err
	}
	r := &Replicas{owners: map[crdt.ReplicaID]string{}}
	for _, rec := range recs {
		id, err := parseReplica(rec.ReplicaID)
		if err != nil {
			return nil, err
		}
		r.owners[crdt.ReplicaID(id)] = rec.UserID
	}
	return r, nil
}

// EnsureReplica implements ports.ReplicaRegistry. Unknown replicas are
// refused: every replica in a dataset is registered.
func (r *Replicas) EnsureReplica(_ context.Context, subject domain.Subject, replica crdt.ReplicaID) error {
	if owner, ok := r.owners[replica]; !ok || owner != subject.UserID {
		return fmt.Errorf("%w: replica %d is not registered to %s", domain.ErrPermissionDenied, replica, subject.UserID)
	}
	return nil
}
