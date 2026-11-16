package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// errActorStopped means the request reached an actor that stopped before
// running it, so retrying with a fresh actor is safe.
var errActorStopped = errors.New("note actor stopped")

// poisoned wraps an error after which the actor's memory may be ahead of the
// database (a failed append after applying ops). The actor stops without
// writing anything else, and the next request loads the note again from the
// store, which holds exactly what was acknowledged.
type poisoned struct{ err error }

func (p poisoned) Error() string { return p.err.Error() }
func (p poisoned) Unwrap() error { return p.err }

type request struct {
	fn    func(*noteState) error
	reply chan error
}

// actor owns one note's state in one goroutine (docs/porting-to-rust.md:
// a Tokio task with an mpsc inbox). Everything that touches the state runs
// as a closure on that goroutine, in arrival order.
type actor struct {
	id    domain.NoteID
	inbox chan request
	quit  chan struct{}
	done  chan struct{}
	// loadErr is set before done closes when loading failed.
	loadErr error
}

// registry routes requests to note actors by note ID, starting an actor on
// first use and evicting idle ones.
type registry struct {
	svc    *Service
	mu     sync.Mutex
	actors map[domain.NoteID]*actor
	wg     sync.WaitGroup
}

func newRegistry(svc *Service) *registry {
	return &registry{svc: svc, actors: map[domain.NoteID]*actor{}}
}

// get returns the note's actor, starting one if none is running.
func (r *registry) get(id domain.NoteID) *actor {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.actors[id]; ok {
		return a
	}
	a := &actor{id: id, inbox: make(chan request, 64), quit: make(chan struct{}), done: make(chan struct{})}
	r.actors[id] = a
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.run(a)
	}()
	return a
}

// loaded returns the note's actor only if one is running.
func (r *registry) loaded(id domain.NoteID) (*actor, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.actors[id]
	return a, ok
}

func (r *registry) all() []*actor {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*actor, 0, len(r.actors))
	for _, a := range r.actors {
		out = append(out, a)
	}
	return out
}

// remove drops a from the routing table if it is still the note's actor.
func (r *registry) remove(a *actor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.actors[a.id] == a {
		delete(r.actors, a.id)
	}
}

// removeIfQuiet removes a only while nothing is queued for it. Callers that
// already hold a and send afterwards see done closed and retry on a new
// actor, so no request is ever served by an actor that is no longer routed.
func (r *registry) removeIfQuiet(a *actor) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(a.inbox) > 0 {
		return false
	}
	if r.actors[a.id] == a {
		delete(r.actors, a.id)
	}
	return true
}

// do runs fn on the note's actor and returns its error. A request that
// reached a stopping actor is retried on a new one; fn has not run then.
func (r *registry) do(ctx context.Context, id domain.NoteID, fn func(*noteState) error) error {
	for attempt := 0; attempt < 3; attempt++ {
		a := r.get(id)
		err := a.call(ctx, fn)
		if !errors.Is(err, errActorStopped) {
			return err
		}
		if a.loadErr != nil {
			return a.loadErr
		}
	}
	return fmt.Errorf("%w: note %s is being moved, retry", domain.ErrUnavailable, id)
}

// ifLoaded runs fn on the note's actor if one is running, without loading
// the note.
func (r *registry) ifLoaded(ctx context.Context, id domain.NoteID, fn func(*noteState) error) error {
	a, ok := r.loaded(id)
	if !ok {
		return nil
	}
	if err := a.call(ctx, fn); err != nil && !errors.Is(err, errActorStopped) {
		return err
	}
	return nil
}

func (a *actor) call(ctx context.Context, fn func(*noteState) error) error {
	req := request{fn: fn, reply: make(chan error, 1)}
	select {
	case a.inbox <- req:
	case <-a.done:
		return errActorStopped
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.reply:
		return err
	case <-a.done:
		// The actor writes the reply before closing done, so a request it
		// ran is never reported as not run.
		select {
		case err := <-req.reply:
			return err
		default:
			return errActorStopped
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run is the actor's goroutine: load, serve requests and ticks, stop.
func (r *registry) run(a *actor) {
	svc := r.svc
	defer close(a.done)
	ctx := context.Background()
	started := time.Now()
	st, err := loadNoteState(ctx, svc, a.id)
	if err != nil {
		a.loadErr = err
		r.remove(a)
		return
	}
	svc.m.loaded(ctx, started)
	svc.m.actors.Add(ctx, 1)
	defer svc.m.actors.Add(ctx, -1)
	ticker := time.NewTicker(svc.cfg.Tick)
	defer ticker.Stop()
	for {
		select {
		case req := <-a.inbox:
			err := req.fn(st)
			req.reply <- err
			if p := (poisoned{}); errors.As(err, &p) {
				svc.logger.Error("note actor stopped after a failed write", "note_id", string(a.id), "error", p.err)
				r.remove(a)
				st.closeSessions(fmt.Errorf("%w: note reloading after a storage error", domain.ErrUnavailable))
				return
			}
		case <-ticker.C:
			if st.tick(ctx) && r.removeIfQuiet(a) {
				st.stop(ctx, nil)
				return
			}
		case <-a.quit:
			r.remove(a)
			st.stop(ctx, fmt.Errorf("%w: server shutting down", domain.ErrUnavailable))
			return
		}
	}
}

// stopAll asks every actor to flush and stop, and waits for them.
func (r *registry) stopAll(ctx context.Context) error {
	for _, a := range r.all() {
		select {
		case <-a.quit:
		default:
			close(a.quit)
		}
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
