package backends

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type fakeResolver struct {
	mu   sync.Mutex
	ips  []string
	fail bool
}

func (f *fakeResolver) set(ips ...string) { f.mu.Lock(); f.ips = ips; f.fail = false; f.mu.Unlock() }

func (f *fakeResolver) LookupHost(context.Context, string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("SERVFAIL")
	}
	return append([]string(nil), f.ips...), nil
}

func newRouter(t *testing.T, resolver *fakeResolver, changes *int) (*SyncRouter, *[]string) {
	t.Helper()
	var dialled []string
	r, err := NewSyncRouter(SyncRouterOptions{
		HostPort: "documents:9090", Resolver: resolver,
		Dial: func(addr string) (*grpc.ClientConn, error) {
			dialled = append(dialled, addr)
			// Lazy: never connects in this test.
			return grpc.NewClient("passthrough:///"+addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		},
		OnChange: func(func(string) (string, bool)) { *changes++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.closeAll)
	return r, &dialled
}

func TestRouterIsUnavailableUntilAnInstanceIsKnown(t *testing.T) {
	changes := 0
	r, _ := newRouter(t, &fakeResolver{}, &changes)
	if _, err := r.Route("n1"); status.Code(err) != codes.Unavailable {
		t.Errorf("got %v", err)
	}
}

func TestRouterRoutesByRendezvousAndReusesConnections(t *testing.T) {
	resolver := &fakeResolver{}
	resolver.set("10.0.0.2", "10.0.0.1")
	changes := 0
	r, dialled := newRouter(t, resolver, &changes)
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner, _ := r.Owner("n1")
	for range 3 {
		route, err := r.Route("n1")
		if err != nil || route.Owner != owner {
			t.Fatalf("route %v, %v", route, err)
		}
	}
	if len(*dialled) != 1 || changes != 1 {
		t.Errorf("dialled %v, changes %d", *dialled, changes)
	}
	// The same members again is not a change.
	_ = r.Refresh(context.Background())
	if changes != 1 {
		t.Errorf("unchanged refresh reported a change")
	}
}

func TestRouterKeepsTheLastMembersWhenDNSFailsAndDropsRemovedInstances(t *testing.T) {
	resolver := &fakeResolver{}
	resolver.set("10.0.0.1", "10.0.0.2")
	changes := 0
	r, _ := newRouter(t, resolver, &changes)
	_ = r.Refresh(context.Background())
	for _, note := range []string{"a", "b", "c", "d", "e", "f"} {
		if _, err := r.Route(note); err != nil {
			t.Fatal(err)
		}
	}
	resolver.mu.Lock()
	resolver.fail = true
	resolver.mu.Unlock()
	if err := r.Refresh(context.Background()); err == nil {
		t.Error("lookup failure not reported")
	}
	if _, err := r.Route("a"); err != nil {
		t.Errorf("lost members on a DNS failure: %v", err)
	}
	resolver.set()
	if err := r.Refresh(context.Background()); err == nil {
		t.Error("empty answer accepted")
	}
	resolver.set("10.0.0.1")
	if err := r.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	_, stale := r.conns["10.0.0.2:9090"]
	r.mu.Unlock()
	if stale || changes != 2 {
		t.Errorf("removed instance still connected: %v, changes %d", stale, changes)
	}
	if owner, _ := r.Owner("a"); owner != "10.0.0.1:9090" {
		t.Errorf("owner %s", owner)
	}
}
