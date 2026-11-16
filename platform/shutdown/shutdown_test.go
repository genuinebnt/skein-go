package shutdown

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"
)

// recorder notes the order in which components stop.
type recorder struct {
	mu    sync.Mutex
	order []string
}

func (r *recorder) note(s string) {
	r.mu.Lock()
	r.order = append(r.order, s)
	r.mu.Unlock()
}

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.order)
}

// blocking returns a run function that signals started and returns when
// cancelled.
func blocking(r *recorder, name string, started *sync.WaitGroup) RunFunc {
	started.Add(1)
	return func(ctx context.Context) error {
		started.Done()
		<-ctx.Done()
		r.note("run " + name + " returned")
		return nil
	}
}

func stopper(r *recorder, name string) StopFunc {
	return func(context.Context) error { r.note("stop " + name); return nil }
}

func TestCancellingContextStopsComponentsInReverseOrder(t *testing.T) {
	r := &recorder{}
	var started sync.WaitGroup
	m := New(Options{Timeout: time.Second})
	m.Add("db", nil, stopper(r, "db"))
	m.Add("relay", blocking(r, "relay", &started), nil)
	m.Add("grpc", blocking(r, "grpc", &started), stopper(r, "grpc"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	started.Wait()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("clean shutdown returned %v", err)
	}
	want := []string{"stop grpc", "run grpc returned", "run relay returned", "stop db"}
	if got := r.got(); !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestSIGTERMStartsShutdown(t *testing.T) {
	r := &recorder{}
	var started sync.WaitGroup
	m := New(Options{Timeout: time.Second})
	m.Add("grpc", blocking(r, "grpc", &started), nil)
	done := make(chan error, 1)
	go func() { done <- m.Run(context.Background()) }()
	started.Wait()
	// Run registered for SIGTERM before starting components, so the signal
	// is caught rather than killing the test binary.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not stop the manager")
	}
}

func TestFailingComponentStopsTheRestAndIsReportedOnce(t *testing.T) {
	r := &recorder{}
	var started sync.WaitGroup
	boom := errors.New("listener closed")
	m := New(Options{Timeout: time.Second})
	m.Add("relay", blocking(r, "relay", &started), nil)
	m.Add("grpc", func(context.Context) error { started.Wait(); return boom }, nil)
	err := m.Run(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("want the component's error, got %v", err)
	}
	if n := countWraps(err, boom); n != 1 {
		t.Fatalf("cause reported %d times: %v", n, err)
	}
	if got := r.got(); !slices.Equal(got, []string{"run relay returned"}) {
		t.Fatalf("other components not stopped: %v", got)
	}
}

func countWraps(err, target error) int {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		if errors.Is(err, target) {
			return 1
		}
		return 0
	}
	n := 0
	for _, e := range joined.Unwrap() {
		n += countWraps(e, target)
	}
	return n
}

func TestComponentExitingEarlyIsAnError(t *testing.T) {
	m := New(Options{Timeout: time.Second})
	m.Add("subscriber", func(context.Context) error { return nil }, nil)
	if err := m.Run(context.Background()); !errors.Is(err, ErrExitedEarly) {
		t.Fatalf("got %v", err)
	}
}

func TestStuckComponentTimesOutAndLaterOnesStillStop(t *testing.T) {
	r := &recorder{}
	release := make(chan struct{})
	defer close(release)
	m := New(Options{Timeout: 100 * time.Millisecond})
	m.Add("db", nil, stopper(r, "db"))
	m.Add("stuck", func(context.Context) error { <-release; return nil }, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := m.Run(ctx)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("shutdown took %v with a 100ms timeout", elapsed)
	}
	if got := r.got(); !slices.Equal(got, []string{"stop db"}) {
		t.Fatalf("components before the stuck one were skipped: %v", got)
	}
}

func TestStopErrorsAreReported(t *testing.T) {
	m := New(Options{Timeout: time.Second})
	failure := errors.New("flush failed")
	m.Add("telemetry", nil, func(context.Context) error { return failure })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Run(ctx); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
}
