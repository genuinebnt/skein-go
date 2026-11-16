package domain

import (
	"errors"
	"fmt"
	"slices"
)

// ErrRingInvariant means a Ring's members are not sorted, unique and
// non-empty.
var ErrRingInvariant = errors.New("domain: ring invariant violated")

// Ring is a set of documents instances ("10.0.3.7:9090") that notes are
// spread over by rendezvous hashing (see the package documentation). The
// zero value is an empty ring.
type Ring struct {
	members []string
}

// NewRing returns a ring over members, ignoring duplicates, empty strings
// and input order.
func NewRing(members []string) Ring {
	sorted := slices.DeleteFunc(slices.Clone(members), func(m string) bool { return m == "" })
	slices.Sort(sorted)
	return Ring{members: slices.Compact(sorted)}
}

// Members returns the members in ascending order.
func (r Ring) Members() []string { return slices.Clone(r.members) }

// Len returns the number of members.
func (r Ring) Len() int { return len(r.members) }

// Equal reports whether two rings have the same members.
func (r Ring) Equal(other Ring) bool { return slices.Equal(r.members, other.members) }

// Owner returns the member that owns key, or false for an empty ring.
func (r Ring) Owner(key string) (string, bool) {
	if len(r.members) == 0 {
		return "", false
	}
	best, bestScore := r.members[0], RendezvousScore(key, r.members[0])
	for _, m := range r.members[1:] {
		// Members are ascending, so a tie keeps the earlier, smaller one.
		if s := RendezvousScore(key, m); s > bestScore {
			best, bestScore = m, s
		}
	}
	return best, true
}

// CheckInvariants verifies that members are sorted, unique and non-empty.
func (r Ring) CheckInvariants() error {
	for i, m := range r.members {
		if m == "" {
			return fmt.Errorf("%w: empty member at %d", ErrRingInvariant, i)
		}
		if i > 0 && r.members[i-1] >= m {
			return fmt.Errorf("%w: %q does not sort after %q", ErrRingInvariant, m, r.members[i-1])
		}
	}
	return nil
}

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// RendezvousScore is h(key, member) from the package documentation:
// splitmix64(FNV-1a-64(key || 0x00 || member)).
func RendezvousScore(key, member string) uint64 {
	h := uint64(fnvOffset64)
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= fnvPrime64
	}
	h ^= 0
	h *= fnvPrime64
	for i := 0; i < len(member); i++ {
		h ^= uint64(member[i])
		h *= fnvPrime64
	}
	return splitmix64(h)
}

// splitmix64 is the finaliser of Steele, Lea and Flood's SplitMix64. FNV-1a
// alone leaves the high bits of short, similar inputs (IP addresses that
// differ in one digit) correlated, which skews the maximum.
func splitmix64(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}
