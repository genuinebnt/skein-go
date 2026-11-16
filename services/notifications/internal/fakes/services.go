package fakes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
	"github.com/genuinebnt/skein-go/services/notifications/internal/email"
)

// Authorizer answers checks with a function, so a test states its policy
// in one place. It records every call.
type Authorizer struct {
	// Decide answers one check for userID at the given time.
	Decide func(userID string, c app.Check, at time.Time) bool
	// Err, when set, fails every call.
	Err error

	mu    sync.Mutex
	calls int
}

// AllowAll returns an Authorizer that allows everything.
func AllowAll() *Authorizer {
	return &Authorizer{Decide: func(string, app.Check, time.Time) bool { return true }}
}

// Authorize implements app.Authorizer.
func (a *Authorizer) Authorize(_ context.Context, userID string, checks []app.Check, at time.Time) ([]bool, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()
	if a.Err != nil {
		return nil, a.Err
	}
	out := make([]bool, len(checks))
	for i, c := range checks {
		out[i] = a.Decide(userID, c, at)
	}
	return out, nil
}

// Calls returns how many Authorize calls were made.
func (a *Authorizer) Calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// DirectoryUser is one user of the fake directory.
type DirectoryUser struct {
	app.Profile
	Handle string
	// Workspaces the user is a member of, for handle resolution.
	Workspaces []string
}

// Directory is an in-memory app.Directory.
type Directory struct {
	mu    sync.Mutex
	users map[string]DirectoryUser
	// Err, when set, fails every call.
	Err error
}

// NewDirectory returns a directory holding users.
func NewDirectory(users ...DirectoryUser) *Directory {
	d := &Directory{users: map[string]DirectoryUser{}}
	for _, u := range users {
		d.users[u.UserID] = u
	}
	return d
}

// Profile implements app.Directory.
func (d *Directory) Profile(_ context.Context, userID string) (app.Profile, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Err != nil {
		return app.Profile{}, d.Err
	}
	u, ok := d.users[userID]
	if !ok {
		return app.Profile{}, fmt.Errorf("%w: %s", app.ErrUnknownUser, userID)
	}
	return u.Profile, nil
}

// ResolveHandles implements app.Directory.
func (d *Directory) ResolveHandles(_ context.Context, workspaceID string, handles []string) (map[string]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Err != nil {
		return nil, d.Err
	}
	out := map[string]string{}
	for _, u := range d.users {
		if slices.Contains(handles, u.Handle) && slices.Contains(u.Workspaces, workspaceID) {
			out[u.Handle] = u.UserID
		}
	}
	return out, nil
}

// Mailer records sent messages. Fail decides per message whether to fail;
// a returned error wrapping app.ErrPermanent is a permanent failure.
type Mailer struct {
	// Fail returns the error for this attempt, or nil to accept the message.
	Fail func(msg email.Message) error
	// Accepted, when set, runs after a message is accepted, as the SMTP
	// server's 250 reply arrives; tests use it to crash right after.
	Accepted func(msg email.Message)

	mu   sync.Mutex
	sent []email.Message
}

// Send implements app.Mailer.
func (m *Mailer) Send(_ context.Context, msg email.Message) error {
	if m.Fail != nil {
		if err := m.Fail(msg); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.sent = append(m.sent, msg)
	m.mu.Unlock()
	if m.Accepted != nil {
		m.Accepted(msg)
	}
	return nil
}

// Sent returns the accepted messages in order.
func (m *Mailer) Sent() []email.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.sent)
}

// FanoutBus is the shared "Redis" of several replicas: each replica's
// Fanout publishes to the bus, and the bus delivers to every replica that
// joined the recipient.
type FanoutBus struct {
	mu       sync.Mutex
	replicas []*Fanout
	// PublishErr, when set, fails every publish.
	PublishErr error
}

// Fanout is one replica's app.Fanout on a FanoutBus.
type Fanout struct {
	bus     *FanoutBus
	deliver func(domain.Notification)
	mu      sync.Mutex
	joined  map[string]bool
}

// Replica attaches a replica whose hub receives notifications through
// deliver. Set deliver with SetDeliver when the hub is built after.
func (b *FanoutBus) Replica() *Fanout {
	f := &Fanout{bus: b, joined: map[string]bool{}}
	b.mu.Lock()
	b.replicas = append(b.replicas, f)
	b.mu.Unlock()
	return f
}

// SetDeliver sets the function messages are delivered to (the hub's
// Deliver).
func (f *Fanout) SetDeliver(deliver func(domain.Notification)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliver = deliver
}

// Publish implements app.Fanout.
func (f *Fanout) Publish(_ context.Context, n domain.Notification) error {
	f.bus.mu.Lock()
	replicas := slices.Clone(f.bus.replicas)
	err := f.bus.PublishErr
	f.bus.mu.Unlock()
	if err != nil {
		return err
	}
	for _, r := range replicas {
		r.mu.Lock()
		joined, deliver := r.joined[n.RecipientID], r.deliver
		r.mu.Unlock()
		if joined && deliver != nil {
			deliver(n)
		}
	}
	return nil
}

// Join implements app.Fanout.
func (f *Fanout) Join(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.joined[userID] {
		return errors.New("fakes: joined twice")
	}
	f.joined[userID] = true
	return nil
}

// Leave implements app.Fanout.
func (f *Fanout) Leave(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.joined[userID] {
		return errors.New("fakes: left without joining")
	}
	delete(f.joined, userID)
	return nil
}

// Joined returns the users this replica has joined, ascending.
func (f *Fanout) Joined() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for u := range f.joined {
		out = append(out, u)
	}
	slices.Sort(out)
	return out
}

// UnreadCache is an app.UnreadCache in a map, counting loads so tests can
// see hits.
type UnreadCache struct {
	mu     sync.Mutex
	counts map[string]uint32
	loads  int
}

// NewUnreadCache returns an empty cache.
func NewUnreadCache() *UnreadCache { return &UnreadCache{counts: map[string]uint32{}} }

// Get implements app.UnreadCache.
func (c *UnreadCache) Get(ctx context.Context, userID string, load func(context.Context) (uint32, error)) (uint32, error) {
	c.mu.Lock()
	if n, ok := c.counts[userID]; ok {
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()
	n, err := load(ctx)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loads++
	c.counts[userID] = n
	return n, nil
}

// Invalidate implements app.UnreadCache.
func (c *UnreadCache) Invalidate(_ context.Context, userIDs ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, u := range userIDs {
		delete(c.counts, u)
	}
	return nil
}

// Loads returns how many times a count was loaded from the store.
func (c *UnreadCache) Loads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loads
}

// Clock is a settable clock.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a clock at t.
func NewClock(t time.Time) *Clock { return &Clock{now: t} }

// Now returns the clock's time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward.
func (c *Clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// IDs returns a deterministic UUIDv7-shaped ID generator: prefix
// distinguishes generators, the counter makes IDs ascend.
func IDs(prefix uint16) app.IDGenerator {
	var mu sync.Mutex
	var n uint64
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("0193%04x-0000-7000-8000-%012x", prefix, n)
	}
}
