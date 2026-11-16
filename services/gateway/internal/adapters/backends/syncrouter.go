package backends

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"

	"github.com/genuinebnt/skein-go/services/gateway/internal/app"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// Resolver looks up the addresses behind a host name; *net.Resolver
// implements it.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// DialFunc opens a connection to one documents instance ("10.0.3.7:9090").
type DialFunc func(addr string) (*grpc.ClientConn, error)

// SyncRouterOptions configure NewSyncRouter.
type SyncRouterOptions struct {
	// HostPort is the documents headless Service ("documents:9090"): its
	// DNS name resolves to one address per ready pod.
	HostPort string
	Resolver Resolver
	Dial     DialFunc
	// Interval between DNS refreshes; default 5 seconds, a little under
	// the kube-dns TTL, so a new pod gets its notes within one interval.
	Interval time.Duration
	// OnChange runs after the members change, with the new owner function.
	// The gateway closes sync sockets whose note moved (app.Registry.Reroute).
	OnChange func(owner func(noteID string) (string, bool))
	Logger   *slog.Logger
}

// SyncRouter implements app.SyncRouter by rendezvous hashing over the
// documents instances found in DNS, with one connection per instance.
//
// Why route here rather than fan out through Redis: documents keeps one
// actor per note that owns its CRDT state, catch-up and presence
// (architecture section 5). If every gateway replica sends a note's
// streams to the same instance, that instance fans operations out to all
// of the note's sockets and no second broadcast layer is needed; with a
// Redis fan-out each gateway would have to re-implement per-client
// catch-up, presence and rejection that documents already does, and two
// documents instances would hold the same note.
type SyncRouter struct {
	host, port string
	opts       SyncRouterOptions

	mu    sync.Mutex
	ring  domain.Ring
	conns map[string]*grpc.ClientConn
}

// NewSyncRouter returns a router; call Refresh or Run before routing.
func NewSyncRouter(opts SyncRouterOptions) (*SyncRouter, error) {
	host, port, err := net.SplitHostPort(opts.HostPort)
	if err != nil {
		return nil, fmt.Errorf("backends: sync target %q: %w", opts.HostPort, err)
	}
	if opts.Interval == 0 {
		opts.Interval = 5 * time.Second
	}
	if opts.Resolver == nil {
		opts.Resolver = net.DefaultResolver
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &SyncRouter{host: host, port: port, opts: opts, conns: map[string]*grpc.ClientConn{}}, nil
}

// Run refreshes the members every Interval until ctx ends, then closes
// every connection.
func (s *SyncRouter) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()
	defer s.closeAll()
	for {
		if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			s.opts.Logger.WarnContext(ctx, "documents instances not refreshed; keeping the last set", slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Refresh resolves the members once. A failed or empty lookup keeps the
// previous members: losing every sync stream because DNS hiccuped would be
// worse than routing to a pod that is going away.
func (s *SyncRouter) Refresh(ctx context.Context) error {
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ips, err := s.opts.Resolver.LookupHost(lookupCtx, s.host)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return fmt.Errorf("backends: %s resolved to no addresses", s.host)
	}
	members := make([]string, len(ips))
	for i, ip := range ips {
		members[i] = net.JoinHostPort(ip, s.port)
	}
	ring := domain.NewRing(members)

	s.mu.Lock()
	changed := !ring.Equal(s.ring)
	var stale []*grpc.ClientConn
	for addr, conn := range s.conns {
		if !slices.Contains(ring.Members(), addr) {
			stale = append(stale, conn)
			delete(s.conns, addr)
		}
	}
	s.ring = ring
	s.mu.Unlock()

	for _, conn := range stale {
		_ = conn.Close()
	}
	if changed {
		s.opts.Logger.InfoContext(ctx, "documents instances changed", slog.Int("instances", ring.Len()))
		if s.opts.OnChange != nil {
			s.opts.OnChange(s.Owner)
		}
	}
	return nil
}

// Owner returns the instance that owns noteID.
func (s *SyncRouter) Owner(noteID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring.Owner(noteID)
}

// Route implements app.SyncRouter. UNAVAILABLE until an instance is known.
func (s *SyncRouter) Route(noteID string) (app.SyncRoute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, ok := s.ring.Owner(noteID)
	if !ok {
		return app.SyncRoute{}, status.Error(codes.Unavailable, "no documents instance is known yet")
	}
	conn, ok := s.conns[owner]
	if !ok {
		var err error
		if conn, err = s.opts.Dial(owner); err != nil {
			return app.SyncRoute{}, status.Errorf(codes.Unavailable, "dial documents instance: %v", err)
		}
		s.conns[owner] = conn
	}
	return app.SyncRoute{Owner: owner, Client: skeinv1.NewDocumentServiceClient(conn)}, nil
}

func (s *SyncRouter) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for addr, conn := range s.conns {
		_ = conn.Close()
		delete(s.conns, addr)
	}
}
