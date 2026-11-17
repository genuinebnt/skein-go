// Package identity reaches the identity service through its generated gRPC
// client: Authorize and ListReadableNotes for permissions, and
// RegisterReplica for replica ownership, and RegisterNote so a new note is
// usable before identity's projection hears of it. Calls carry the caller's bearer
// token (grpcx forwards it), so identity sees the same subject documents
// does (ADR-0008, ADR-0009).
package identity

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/ports"
)

// Client implements ports.Authorizer, ports.NoteRegistry and
// ports.ReplicaRegistry.
type Client struct {
	rpc skeinv1.IdentityServiceClient
}

// New wraps a generated client.
func New(rpc skeinv1.IdentityServiceClient) *Client { return &Client{rpc: rpc} }

var (
	_ ports.Authorizer      = (*Client)(nil)
	_ ports.ReplicaRegistry = (*Client)(nil)
	_ ports.NoteRegistry    = (*Client)(nil)
)

// RegisterNote implements ports.NoteRegistry. The caller's token travels
// with ctx, so identity records the caller as the creator after checking
// they may create notes in the workspace.
func (c *Client) RegisterNote(ctx context.Context, _ domain.Subject, note domain.NoteID, ws domain.WorkspaceID) error {
	if _, err := c.rpc.RegisterNote(ctx, &skeinv1.RegisterNoteRequest{NoteId: string(note), WorkspaceId: string(ws)}); err != nil {
		return fmt.Errorf("identity: register note: %w", err)
	}
	return nil
}

var actions = map[ports.Action]skeinv1.Action{
	ports.ActionRead:   skeinv1.Action_ACTION_NOTE_READ,
	ports.ActionEdit:   skeinv1.Action_ACTION_NOTE_EDIT,
	ports.ActionCreate: skeinv1.Action_ACTION_NOTE_CREATE,
	ports.ActionDelete: skeinv1.Action_ACTION_NOTE_DELETE,
}

func subject(s domain.Subject) *skeinv1.Subject {
	return &skeinv1.Subject{UserId: s.UserID, AgentRunId: s.AgentRunID}
}

// Authorize implements ports.Authorizer.
func (c *Client) Authorize(ctx context.Context, s domain.Subject, checks []ports.Check) ([]bool, error) {
	req := &skeinv1.AuthorizeRequest{Subject: subject(s)}
	for _, ch := range checks {
		a, ok := actions[ch.Action]
		if !ok {
			return nil, fmt.Errorf("identity: unknown action %d", ch.Action)
		}
		req.Checks = append(req.Checks, &skeinv1.Check{Action: a, ResourceId: ch.Resource})
	}
	resp, err := c.rpc.Authorize(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("identity: authorize: %w", err)
	}
	return resp.GetAllowed(), nil
}

// ReadableNotes implements ports.Authorizer.
func (c *Client) ReadableNotes(ctx context.Context, s domain.Subject, ws domain.WorkspaceID) ([]domain.NoteID, error) {
	resp, err := c.rpc.ListReadableNotes(ctx, &skeinv1.ListReadableNotesRequest{Subject: subject(s), WorkspaceId: string(ws)})
	if err != nil {
		return nil, fmt.Errorf("identity: readable notes: %w", err)
	}
	out := make([]domain.NoteID, len(resp.GetNoteIds()))
	for i, id := range resp.GetNoteIds() {
		out[i] = domain.NoteID(id)
	}
	return out, nil
}

// EnsureReplica implements ports.ReplicaRegistry with RegisterReplica, which
// succeeds for the caller's own replica and is ALREADY_EXISTS for someone
// else's (docs/api/identity.md).
func (c *Client) EnsureReplica(ctx context.Context, _ domain.Subject, replica crdt.ReplicaID) error {
	_, err := c.rpc.RegisterReplica(ctx, &skeinv1.RegisterReplicaRequest{ReplicaId: uint64(replica)})
	switch status.Code(err) {
	case codes.OK:
		return nil
	case codes.AlreadyExists:
		return fmt.Errorf("%w: replica %d belongs to another user", domain.ErrPermissionDenied, replica)
	default:
		return fmt.Errorf("%w: identity: register replica: %v", domain.ErrUnavailable, err)
	}
}
