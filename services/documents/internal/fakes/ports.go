package fakes

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/ports"
)

// Role is a fake user's role on a note.
type Role uint8

// Roles, in increasing power.
const (
	NoAccess Role = iota
	Viewer
	Editor
	Owner // may delete and restore
)

// Authorizer is an ports.Authorizer over an explicit role table, standing in
// for identity. Changes take effect at once (no cache).
type Authorizer struct {
	mu        sync.Mutex
	roles     map[string]map[string]Role // note -> user -> role
	workspace map[string]domain.WorkspaceID
	creators  map[domain.WorkspaceID]map[string]bool
	calls     int
}

// NewAuthorizer returns an authorizer that allows nothing.
func NewAuthorizer() *Authorizer {
	return &Authorizer{roles: map[string]map[string]Role{}, workspace: map[string]domain.WorkspaceID{}, creators: map[domain.WorkspaceID]map[string]bool{}}
}

var _ ports.Authorizer = (*Authorizer)(nil)

// AllowCreate lets user create notes in workspace.
func (a *Authorizer) AllowCreate(ws domain.WorkspaceID, user string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.creators[ws] == nil {
		a.creators[ws] = map[string]bool{}
	}
	a.creators[ws][user] = true
}

// SetRole gives user role on a note in workspace ws.
func (a *Authorizer) SetRole(ws domain.WorkspaceID, note domain.NoteID, user string, role Role) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.roles[string(note)] == nil {
		a.roles[string(note)] = map[string]Role{}
	}
	a.roles[string(note)][user] = role
	a.workspace[string(note)] = ws
}

// Calls returns how many Authorize calls were made.
func (a *Authorizer) Calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// Authorize implements ports.Authorizer. An agent run is decided as its user
// (ADR-0009).
func (a *Authorizer) Authorize(_ context.Context, subject domain.Subject, checks []ports.Check) ([]bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	out := make([]bool, len(checks))
	for i, c := range checks {
		role := a.roles[c.Resource][subject.UserID]
		switch c.Action {
		case ports.ActionRead:
			out[i] = role >= Viewer
		case ports.ActionEdit:
			out[i] = role >= Editor
		case ports.ActionDelete:
			out[i] = role >= Owner
		case ports.ActionCreate:
			out[i] = a.creators[domain.WorkspaceID(c.Resource)][subject.UserID]
		}
	}
	return out, nil
}

// ReadableNotes implements ports.Authorizer.
func (a *Authorizer) ReadableNotes(_ context.Context, subject domain.Subject, ws domain.WorkspaceID) ([]domain.NoteID, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []domain.NoteID
	for note, users := range a.roles {
		if a.workspace[note] == ws && users[subject.UserID] >= Viewer {
			out = append(out, domain.NoteID(note))
		}
	}
	slices.Sort(out)
	return out, nil
}

// Replicas is an ports.ReplicaRegistry with RegisterReplica's semantics: the
// first user to present a replica owns it.
type Replicas struct {
	mu     sync.Mutex
	owners map[crdt.ReplicaID]string
}

// NewReplicas returns an empty registry.
func NewReplicas() *Replicas { return &Replicas{owners: map[crdt.ReplicaID]string{}} }

// Register records an owner, as the seed's replicas.jsonl does.
func (r *Replicas) Register(replica crdt.ReplicaID, user string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owners[replica] = user
}

// EnsureReplica implements ports.ReplicaRegistry.
func (r *Replicas) EnsureReplica(_ context.Context, subject domain.Subject, replica crdt.ReplicaID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	owner, ok := r.owners[replica]
	if !ok {
		r.owners[replica] = subject.UserID
		return nil
	}
	if owner != subject.UserID {
		return fmt.Errorf("%w: replica %d belongs to another user", domain.ErrPermissionDenied, replica)
	}
	return nil
}

// NoteCache is an ports.NoteCache in a map.
type NoteCache struct {
	mu    sync.Mutex
	notes map[domain.NoteID]domain.Note
}

// NewNoteCache returns an empty cache.
func NewNoteCache() *NoteCache { return &NoteCache{notes: map[domain.NoteID]domain.Note{}} }

// Get implements ports.NoteCache.
func (c *NoteCache) Get(_ context.Context, id domain.NoteID) (domain.Note, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.notes[id]
	return n, ok
}

// Set implements ports.NoteCache.
func (c *NoteCache) Set(_ context.Context, n domain.Note) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notes[n.ID] = n
}

// Delete implements ports.NoteCache.
func (c *NoteCache) Delete(_ context.Context, id domain.NoteID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.notes, id)
}

// Presence is an ports.PresenceStore with expiry on a clock.
type Presence struct {
	mu      sync.Mutex
	clock   ports.Clock
	entries map[domain.NoteID]map[crdt.ReplicaID]presenceEntry
}

type presenceEntry struct {
	p       ports.Presence
	expires time.Time
}

// NewPresence returns an empty presence store on clock.
func NewPresence(clock ports.Clock) *Presence {
	return &Presence{clock: clock, entries: map[domain.NoteID]map[crdt.ReplicaID]presenceEntry{}}
}

// Put implements ports.PresenceStore.
func (p *Presence) Put(_ context.Context, id domain.NoteID, pr ports.Presence, ttl time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries[id] == nil {
		p.entries[id] = map[crdt.ReplicaID]presenceEntry{}
	}
	p.entries[id][pr.Replica] = presenceEntry{pr, p.clock.Now().Add(ttl)}
	return nil
}

// List implements ports.PresenceStore, ordered by replica.
func (p *Presence) List(_ context.Context, id domain.NoteID) ([]ports.Presence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []ports.Presence
	for _, e := range p.entries[id] {
		if p.clock.Now().Before(e.expires) {
			out = append(out, e.p)
		}
	}
	slices.SortFunc(out, func(a, b ports.Presence) int { return cmp.Compare(a.Replica, b.Replica) })
	return out, nil
}

// Remove implements ports.PresenceStore.
func (p *Presence) Remove(_ context.Context, id domain.NoteID, replica crdt.ReplicaID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.entries[id], replica)
	return nil
}

// Clock is a settable ports.Clock.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock showing t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now implements ports.Clock.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Set moves the clock to t.
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
