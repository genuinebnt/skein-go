// Package clients adapts identity's and ml's generated gRPC clients to the
// search service's ports. Calls forward the caller's token and request ID
// (grpcx clients do this from the context), so identity and ml apply the
// caller's own permissions too.
package clients

import (
	"context"
	"fmt"
	"math"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/services/search/internal/app"
)

// mapError turns a status from another service into an app error class.
// NOT_FOUND and PERMISSION_DENIED both mean "the caller may not see this",
// which search reports as NOT_FOUND.
func mapError(service string, err error, resourceType, name string) error {
	switch status.Code(err) {
	case codes.NotFound, codes.PermissionDenied:
		return &app.NotFoundError{ResourceType: resourceType, Name: name}
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		return fmt.Errorf("%w: %s: %v", app.ErrUnavailable, service, err)
	}
	return fmt.Errorf("%s: %w", service, err)
}

// Identity is an app.AccessDirectory over IdentityService.
type Identity struct {
	client skeinv1.IdentityServiceClient
}

// NewIdentity wraps a client.
func NewIdentity(client skeinv1.IdentityServiceClient) *Identity { return &Identity{client: client} }

// ListReadableNotes implements app.AccessDirectory.
func (i *Identity) ListReadableNotes(ctx context.Context, subject auth.Subject, workspace string) ([]string, error) {
	resp, err := i.client.ListReadableNotes(ctx, &skeinv1.ListReadableNotesRequest{
		Subject: &skeinv1.Subject{UserId: subject.UserID, AgentRunId: subject.AgentRunID}, WorkspaceId: workspace})
	if err != nil {
		return nil, mapError("identity", err, "skein.v1.Workspace", workspace)
	}
	return resp.GetNoteIds(), nil
}

// ML implements app.Relevance and app.Similarity over MLService.
type ML struct {
	client skeinv1.MLServiceClient
	// MaxNotes bounds how many notes NoteCosines asks about (one Discover
	// call each); links between other notes weigh as unrelated.
	MaxNotes int
	// RelatedPerNote is Discover's k.
	RelatedPerNote int
}

// NewML wraps a client.
func NewML(client skeinv1.MLServiceClient) *ML {
	return &ML{client: client, MaxNotes: 200, RelatedPerNote: 50}
}

// TopicRelevance implements app.Relevance: semantic search for the topic,
// each note scored by its best block's cosine, clamped to [0, 1].
func (m *ML) TopicRelevance(ctx context.Context, workspace, topic string) (map[string]float64, error) {
	resp, err := m.client.SemanticSearch(ctx, &skeinv1.SemanticSearchRequest{WorkspaceId: workspace, Query: topic, K: 100})
	if err != nil {
		return nil, mapError("ml", err, "skein.v1.Workspace", workspace)
	}
	out := map[string]float64{}
	for _, h := range resp.GetHits() {
		note := h.GetLocation().GetNoteId()
		out[note] = math.Max(out[note], math.Max(0, math.Min(1, h.GetCosine())))
	}
	return out, nil
}

// NoteCosines implements app.Similarity. MLService has no pairwise
// similarity RPC (a contract follow-up), so it asks Discover for each
// note's nearest neighbours and keeps the cosines of the requested pairs
// it finds there. A linked pair outside each other's top RelatedPerNote is
// left out, and weighs as unrelated.
func (m *ML) NoteCosines(ctx context.Context, _ string, pairs []app.NotePair) (map[app.NotePair]float64, error) {
	want := map[app.NotePair]bool{}
	var notes []string
	seen := map[string]bool{}
	for _, p := range pairs {
		want[p] = true
		for _, n := range []string{p.From, p.To} {
			if !seen[n] && len(notes) < m.MaxNotes {
				seen[n] = true
				notes = append(notes, n)
			}
		}
	}
	out := map[app.NotePair]float64{}
	for _, n := range notes {
		resp, err := m.client.Discover(ctx, &skeinv1.DiscoverRequest{NoteId: n, K: uint32(m.RelatedPerNote)})
		if err != nil {
			if status.Code(err) == codes.NotFound {
				continue
			}
			return nil, mapError("ml", err, "skein.v1.Note", n)
		}
		for _, r := range resp.GetRelated() {
			p := app.NotePair{From: min(n, r.GetNoteId()), To: max(n, r.GetNoteId())}
			if want[p] {
				out[p] = r.GetCosine()
			}
		}
	}
	return out, nil
}
