// Package shutdown runs a service's long-lived components and stops them
// in reverse order when the process is asked to stop.
//
// # Model
//
// A component has a name, an optional run function and an optional stop
// function. Run starts every run function at once, each on its own context.
// Shutdown begins when the first of these happens:
//
//   - SIGINT or SIGTERM arrives (Kubernetes sends SIGTERM on pod deletion);
//   - the context passed to Run is cancelled;
//   - any run function returns, with or without an error, because a
//     service missing a component is broken and should restart.
//
// Components are then stopped one at a time, last added first: for each,
// its stop function is called (if any), its run context is cancelled, and
// Run waits for its run function to return. So add components in
// dependency order (database pool, then the outbox relay, then the gRPC
// server) and they stop in the safe order: the server stops taking calls
// before the relay stops, and the pool closes last.
//
// One Timeout covers the whole shutdown, and should be a little shorter
// than the pod's terminationGracePeriodSeconds (30 seconds by default, so
// the default is 25). Components still running when it expires are
// reported in the error and abandoned; the process is about to exit.
//
// # Errors
//
// Run returns nil after a clean shutdown caused by a signal or by ctx. It
// returns the error of the component that caused the shutdown, joined with
// any stop errors and ErrTimeout. A component that returns nil on its own
// is reported as ErrExitedEarly.
//
// # Porting notes
//
// Rust: `tokio::signal` plus a `CancellationToken` per component and a
// `JoinSet`; stop in reverse with `tokio::time::timeout` around the lot.
package shutdown

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Errors returned by Run. Test with errors.Is.
var (
	// ErrTimeout means some components had not stopped when Timeout
	// expired.
	ErrTimeout = errors.New("shutdown: timed out")
	// ErrExitedEarly means a component's run function returned nil before
	// shutdown was requested.
	ErrExitedEarly = errors.New("shutdown: component exited before shutdown")
)

// RunFunc runs until ctx is cancelled, then returns nil, or returns an
// error if it fails.
type RunFunc func(ctx context.Context) error

// StopFunc releases a component. It should return before ctx's deadline.
type StopFunc func(ctx context.Context) error

// Options configure New. Zero values take the defaults.
type Options struct {
	Timeout time.Duration // 25s
	// Signals that start shutdown; SIGINT and SIGTERM when nil.
	Signals []os.Signal
	Logger  *slog.Logger
}

type component struct {
	name   string
	run    RunFunc
	stop   StopFunc
	cancel context.CancelFunc
	done   chan error
	// exited is set for the component whose exit caused the shutdown, so
	// its error is reported once, as the cause.
	exited bool
}

// Manager runs components. Add every component before calling Run.
type Manager struct {
	opts       Options
	components []*component
}

// New returns an empty manager.
func New(opts Options) *Manager {
	if opts.Timeout <= 0 {
		opts.Timeout = 25 * time.Second
	}
	if opts.Signals == nil {
		opts.Signals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Manager{opts: opts}
}

// Add registers a component. Either function may be nil: a server has run
// (Serve until cancelled) and often no stop; a connection pool has only
// stop (Close).
func (m *Manager) Add(name string, run RunFunc, stop StopFunc) {
	m.components = append(m.components, &component{name: name, run: run, stop: stop})
}

type exit struct {
	name      string
	err       error
	component *component
}

// Run starts every component and blocks until shutdown has finished.
func (m *Manager) Run(ctx context.Context) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, m.opts.Signals...)
	defer signal.Stop(signals)

	exits := make(chan exit, len(m.components))
	for _, c := range m.components {
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		c.cancel = cancel
		c.done = make(chan error, 1)
		if c.run == nil {
			close(c.done)
			continue
		}
		go func() {
			err := c.run(runCtx)
			c.done <- err
			exits <- exit{name: c.name, err: err, component: c}
		}()
	}

	var cause error
	select {
	case sig := <-signals:
		m.opts.Logger.Info("shutting down", slog.String("reason", "signal"), slog.String("signal", sig.String()))
	case <-ctx.Done():
		m.opts.Logger.Info("shutting down", slog.String("reason", "context cancelled"))
	case e := <-exits:
		e.component.exited = true
		cause = e.err
		if cause == nil {
			cause = fmt.Errorf("%w: %s", ErrExitedEarly, e.name)
		} else {
			cause = fmt.Errorf("shutdown: %s: %w", e.name, cause)
		}
		m.opts.Logger.Error("shutting down", slog.String("reason", "component exited"),
			slog.String("component", e.name), slog.String("error", cause.Error()))
	}
	return errors.Join(cause, m.stopAll())
}

// stopAll stops components in reverse order under one deadline.
func (m *Manager) stopAll() error {
	ctx, cancel := context.WithTimeout(context.Background(), m.opts.Timeout)
	defer cancel()
	var errs []error
	var stuck []string
	for i := len(m.components) - 1; i >= 0; i-- {
		c := m.components[i]
		start := time.Now()
		if c.stop != nil {
			if err := c.stop(ctx); err != nil {
				errs = append(errs, fmt.Errorf("shutdown: stop %s: %w", c.name, err))
			}
		}
		c.cancel()
		stopped, err := waitDone(ctx, c.done)
		if !stopped {
			stuck = append(stuck, c.name)
			m.opts.Logger.Error("component did not stop in time", slog.String("component", c.name))
			continue
		}
		if err != nil && !c.exited && !errors.Is(err, context.Canceled) {
			errs = append(errs, fmt.Errorf("shutdown: %s: %w", c.name, err))
		}
		m.opts.Logger.Info("component stopped", slog.String("component", c.name), slog.Duration("took", time.Since(start)))
	}
	if len(stuck) > 0 {
		errs = append(errs, fmt.Errorf("%w: %v", ErrTimeout, stuck))
	}
	return errors.Join(errs...)
}

// waitDone waits for a run function's result until ctx ends. A result that
// is already there wins over an expired deadline.
func waitDone(ctx context.Context, done <-chan error) (bool, error) {
	select {
	case err := <-done:
		return true, err
	default:
	}
	select {
	case err := <-done:
		return true, err
	case <-ctx.Done():
		return false, nil
	}
}
