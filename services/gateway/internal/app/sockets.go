package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// ErrDraining means the gateway is shutting down and takes no new sockets.
var ErrDraining = errors.New("app: gateway is draining")

// ErrRegistryInvariant means Registry.CheckInvariants found a bad entry.
var ErrRegistryInvariant = errors.New("app: socket registry invariant violated")

// SocketCloser closes a live socket. Close must not block: it records the
// code and wakes the socket's own goroutines, which do the closing
// handshake. Only the first call's code counts.
type SocketCloser interface {
	Close(code domain.CloseCode, reason string)
}

// SocketInfo describes a live socket.
type SocketInfo struct {
	Kind   domain.SocketKind
	UserID string
	// NoteID and Owner (the documents instance) are set for sync sockets.
	NoteID string
	Owner  string
}

type entry struct {
	info   SocketInfo
	closer SocketCloser
	// credentials is the authenticated context the socket was opened with
	// (verified claims and token, no cancellation), so a revocation can
	// re-check access as the socket's user.
	credentials context.Context
}

// Registry tracks live sockets.
type Registry struct {
	mu       sync.Mutex
	next     uint64
	sockets  map[uint64]entry
	draining bool
	drained  chan struct{}
	// peak is the count when draining began; it only shrinks after that.
	peak int
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{sockets: map[uint64]entry{}, drained: make(chan struct{})}
}

// Add registers a socket and returns the function that removes it, to be
// called exactly once when the socket has closed. credentials is the
// socket's authenticated context (auth.WithClaims), kept for re-checking
// access on revocation. ErrDraining once Drain has begun.
func (r *Registry) Add(credentials context.Context, info SocketInfo, closer SocketCloser) (remove func(), err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.draining {
		return nil, ErrDraining
	}
	r.next++
	id := r.next
	r.sockets[id] = entry{info: info, closer: closer, credentials: credentials}
	var once sync.Once
	return func() { once.Do(func() { r.remove(id) }) }, nil
}

func (r *Registry) remove(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sockets, id)
	if r.draining && len(r.sockets) == 0 {
		r.closeDrainedLocked()
	}
}

func (r *Registry) closeDrainedLocked() {
	select {
	case <-r.drained:
	default:
		close(r.drained)
	}
}

// closeMatching closes every socket for which match is true and returns
// how many it closed.
func (r *Registry) closeMatching(code domain.CloseCode, reason string, match func(SocketInfo) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.sockets {
		if match(e.info) {
			e.closer.Close(code, reason)
			n++
		}
	}
	return n
}

// affected returns a snapshot of the sockets rev affects, so callers can
// re-check access over the network without holding the lock. A socket in
// the snapshot may close meanwhile; closing it again is a no-op.
func (r *Registry) affected(rev domain.Revocation) []entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []entry
	for _, e := range r.sockets {
		if rev.Affects(e.info.Kind, e.info.UserID, e.info.NoteID) {
			out = append(out, e)
		}
	}
	return out
}

// Reroute closes, with 1012, every sync socket whose note is now owned by
// a different documents instance, so the client reconnects to the new
// owner and only one instance holds each note's actor.
func (r *Registry) Reroute(owner func(noteID string) (string, bool)) int {
	return r.closeMatching(domain.CloseServiceRestart, "note moved to another instance", func(info SocketInfo) bool {
		if info.Kind != domain.SocketSync {
			return false
		}
		now, ok := owner(info.NoteID)
		return ok && now != info.Owner
	})
}

// Drain refuses new sockets, closes every open one with 1012 and waits
// until all have been removed or ctx ends.
func (r *Registry) Drain(ctx context.Context) error {
	r.mu.Lock()
	if !r.draining {
		r.draining = true
		r.peak = len(r.sockets)
	}
	for _, e := range r.sockets {
		e.closer.Close(domain.CloseServiceRestart, "gateway restarting")
	}
	if len(r.sockets) == 0 {
		r.closeDrainedLocked()
	}
	r.mu.Unlock()
	select {
	case <-r.drained:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("app: %d sockets still open: %w", r.Len(), ctx.Err())
	}
}

// Len returns the number of live sockets.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sockets)
}

// CountByKind returns the number of live sockets of one kind.
func (r *Registry) CountByKind(kind domain.SocketKind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.sockets {
		if e.info.Kind == kind {
			n++
		}
	}
	return n
}

// CheckInvariants verifies the entries (see the package documentation).
func (r *Registry) CheckInvariants() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, e := range r.sockets {
		switch {
		case e.closer == nil:
			return fmt.Errorf("%w: socket %d has no closer", ErrRegistryInvariant, id)
		case e.info.UserID == "":
			return fmt.Errorf("%w: socket %d has no user", ErrRegistryInvariant, id)
		case e.info.Kind == domain.SocketSync && (e.info.NoteID == "" || e.info.Owner == ""):
			return fmt.Errorf("%w: sync socket %d lacks note or owner", ErrRegistryInvariant, id)
		case e.info.Kind != domain.SocketSync && e.info.Kind != domain.SocketAgent && e.info.Kind != domain.SocketNotifications:
			return fmt.Errorf("%w: socket %d has kind %q", ErrRegistryInvariant, id, e.info.Kind)
		}
	}
	if r.draining && len(r.sockets) > r.peak {
		return fmt.Errorf("%w: %d sockets while draining from %d", ErrRegistryInvariant, len(r.sockets), r.peak)
	}
	return nil
}
