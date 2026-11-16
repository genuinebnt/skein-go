// Package backends connects the gateway to the services behind it.
//
// Clients holds one generated client per service over connections made by
// platform/grpcx: "dns:///host:port" targets resolved per call with
// round_robin, keepalive, request ID and bearer token forwarding, and
// retries on UNAVAILABLE only for the RPCs listed in IdempotentMethods
// (reads, PUTs, and the operations the API documents as idempotent).
// GraphService is served by search, so both share one connection.
//
// SyncRouter (syncrouter.go) is separate because sync streams must reach
// the one documents instance that owns a note: it resolves the documents
// headless Service itself and keeps a connection per instance.
//
// # Porting notes
//
// Rust: one tonic Channel per service from a balanced endpoint list; the
// idempotent-method list becomes a tower::retry policy.
package backends

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/grpcx"
)

// Clients are the generated clients the gateway calls.
type Clients struct {
	Identity      skeinv1.IdentityServiceClient
	Documents     skeinv1.DocumentServiceClient
	Search        skeinv1.SearchServiceClient
	Graph         skeinv1.GraphServiceClient
	ML            skeinv1.MLServiceClient
	Agent         skeinv1.AgentServiceClient
	Notifications skeinv1.NotificationServiceClient
}

// Conns are the connections behind Clients, one per service.
type Conns struct {
	Identity, Documents, Search, ML, Agent, Notifications grpc.ClientConnInterface
}

// NewClients wraps connections in generated clients.
func NewClients(c Conns) Clients {
	return Clients{
		Identity:      skeinv1.NewIdentityServiceClient(c.Identity),
		Documents:     skeinv1.NewDocumentServiceClient(c.Documents),
		Search:        skeinv1.NewSearchServiceClient(c.Search),
		Graph:         skeinv1.NewGraphServiceClient(c.Search),
		ML:            skeinv1.NewMLServiceClient(c.ML),
		Agent:         skeinv1.NewAgentServiceClient(c.Agent),
		Notifications: skeinv1.NewNotificationServiceClient(c.Notifications),
	}
}

// Targets are the gRPC targets of the services.
type Targets struct {
	Identity, Documents, Search, ML, Agent, Notifications string
}

// IdempotentMethods are retried on UNAVAILABLE. Never add a method whose
// repetition has an effect (creating, appending, sending).
var IdempotentMethods = []string{
	"skein.v1.SearchService",
	"skein.v1.GraphService",
	"skein.v1.IdentityService/GetUser",
	"skein.v1.IdentityService/ListMembers",
	"skein.v1.IdentityService/SetWorkspaceRole",
	"skein.v1.IdentityService/SetNoteRole",
	"skein.v1.IdentityService/Authorize",
	"skein.v1.IdentityService/RegisterReplica",
	"skein.v1.DocumentService/GetNote",
	"skein.v1.DocumentService/ListNotes",
	"skein.v1.DocumentService/ListVersions",
	"skein.v1.DocumentService/GetTextAt",
	"skein.v1.DocumentService/Diff",
	"skein.v1.DocumentService/Blame",
	"skein.v1.DocumentService/ApplyOps",
	"skein.v1.MLService/SemanticSearch",
	"skein.v1.MLService/Discover",
	"skein.v1.MLService/ListClusters",
	"skein.v1.AgentService/ListChats",
	"skein.v1.AgentService/GetRun",
	"skein.v1.NotificationService/ListInbox",
	"skein.v1.NotificationService/MarkRead",
	"skein.v1.NotificationService/GetPreferences",
	"skein.v1.NotificationService/UpdatePreferences",
}

// DialOptions are shared by every connection.
type DialOptions struct {
	Insecure       bool
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
}

// Dial connects to every target. Connections are lazy, so this does not
// wait for the services; close the returned connections at shutdown.
func Dial(t Targets, opts DialOptions) (Conns, []*grpc.ClientConn, error) {
	var c Conns
	services := []struct {
		name, target string
		into         *grpc.ClientConnInterface
	}{
		{"identity", t.Identity, &c.Identity}, {"documents", t.Documents, &c.Documents},
		{"search", t.Search, &c.Search}, {"ml", t.ML, &c.ML},
		{"agent", t.Agent, &c.Agent}, {"notifications", t.Notifications, &c.Notifications},
	}
	var all []*grpc.ClientConn
	for _, svc := range services {
		conn, err := grpcx.NewClient(grpcx.ClientConfig{
			Target: svc.target, IdempotentMethods: IdempotentMethods, Insecure: opts.Insecure,
			TracerProvider: opts.TracerProvider, MeterProvider: opts.MeterProvider,
		})
		if err != nil {
			for _, open := range all {
				_ = open.Close()
			}
			return Conns{}, nil, fmt.Errorf("backends: %s: %w", svc.name, err)
		}
		all = append(all, conn)
		*svc.into = conn
	}
	return c, all, nil
}

// NoteAccess implements app.NoteAccess with IdentityService.Authorize.
type NoteAccess struct {
	Identity skeinv1.IdentityServiceClient
}

// CanRead implements app.NoteAccess.
func (a NoteAccess) CanRead(ctx context.Context, subject auth.Subject, noteID string) (bool, error) {
	resp, err := a.Identity.Authorize(ctx, &skeinv1.AuthorizeRequest{
		Subject: &skeinv1.Subject{UserId: subject.UserID, AgentRunId: subject.AgentRunID},
		Checks:  []*skeinv1.Check{{Action: skeinv1.Action_ACTION_NOTE_READ, ResourceId: noteID}},
	})
	if err != nil {
		return false, err
	}
	allowed := resp.GetAllowed()
	return len(allowed) == 1 && allowed[0], nil
}
