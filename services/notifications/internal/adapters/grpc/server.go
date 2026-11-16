// Package grpc serves skein.v1.NotificationService (proto/skein/v1/
// notifications.proto, docs/api/notifications.md) on a platform/grpcx
// server, which authenticates every call: each RPC acts on the verified
// subject's own inbox (for an agent run, the user it acts for).
//
// Errors map at this edge only: domain.ErrInvalidArgument is
// INVALID_ARGUMENT, a missing subject UNAUTHENTICATED, a subscriber that
// fell behind RESOURCE_EXHAUSTED (resubscribe and reload the inbox), a
// cancelled or expired context CANCELLED or DEADLINE_EXCEEDED, and anything
// else INTERNAL with a generic message (the cause is logged, never sent).
//
// Subscribe streams the caller's new inbox notifications until the client
// goes away; the gateway relays it as the /v1/notifications/live WebSocket.
// It sends nothing on connect: the client loads ListInbox first, then
// applies pushed notifications by ID (a refreshed notification arrives
// again with the same ID).
//
// # Porting notes
//
// Rust: a tonic service; Subscribe returns a ReceiverStream fed by the
// hub's mpsc channel.
package grpc

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
)

// Server implements skeinv1.NotificationServiceServer.
type Server struct {
	skeinv1.UnimplementedNotificationServiceServer
	inbox  *app.Inbox
	hub    *app.Hub
	logger *slog.Logger
}

// New returns the server.
func New(inbox *app.Inbox, hub *app.Hub, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Server{inbox: inbox, hub: hub, logger: logger}
}

// Register adds the service to s.
func (s *Server) Register(registrar grpc.ServiceRegistrar) {
	skeinv1.RegisterNotificationServiceServer(registrar, s)
}

func caller(ctx context.Context) (string, error) {
	subject, ok := auth.SubjectFromContext(ctx)
	if !ok || subject.UserID == "" {
		return "", status.Error(codes.Unauthenticated, "no authenticated caller")
	}
	return subject.UserID, nil
}

func (s *Server) toStatus(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	}
	s.logger.ErrorContext(ctx, "notification rpc failed", slog.String("error", err.Error()))
	return status.Error(codes.Internal, "internal error")
}

// ToProto converts a notification to its API form.
func ToProto(n domain.Notification) *skeinv1.Notification {
	out := &skeinv1.Notification{
		Id: n.ID, Kind: skeinv1.NotificationKind(n.Kind), Title: n.Title, Body: n.Body, ActorId: n.ActorID,
		CreatedAt: timestamppb.New(n.CreatedAt), Read: n.Read(),
	}
	if !n.Location.IsZero() {
		out.Location = &skeinv1.BlockRef{NoteId: n.Location.NoteID, BlockId: n.Location.BlockID, Start: n.Location.Start, End: n.Location.End}
	}
	return out
}

// ListInbox implements the RPC.
func (s *Server) ListInbox(ctx context.Context, req *skeinv1.ListInboxRequest) (*skeinv1.ListInboxResponse, error) {
	user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.inbox.ListInbox(ctx, user, req.GetPage().GetPageSize(), req.GetPage().GetPageToken(), req.GetUnreadOnly())
	if err != nil {
		return nil, s.toStatus(ctx, err)
	}
	resp := &skeinv1.ListInboxResponse{Page: &skeinv1.PageResponse{NextPageToken: page.NextPageToken}, Unread: page.Unread}
	for _, n := range page.Notifications {
		resp.Notifications = append(resp.Notifications, ToProto(n))
	}
	return resp, nil
}

// MarkRead implements the RPC.
func (s *Server) MarkRead(ctx context.Context, req *skeinv1.MarkReadRequest) (*skeinv1.MarkReadResponse, error) {
	user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.inbox.MarkRead(ctx, user, req.GetIds()); err != nil {
		return nil, s.toStatus(ctx, err)
	}
	return &skeinv1.MarkReadResponse{}, nil
}

// GetPreferences implements the RPC.
func (s *Server) GetPreferences(ctx context.Context, _ *skeinv1.GetPreferencesRequest) (*skeinv1.GetPreferencesResponse, error) {
	user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	prefs, err := s.inbox.Preferences(ctx, user)
	if err != nil {
		return nil, s.toStatus(ctx, err)
	}
	resp := &skeinv1.GetPreferencesResponse{}
	for _, p := range prefs.List() {
		resp.Preferences = append(resp.Preferences, &skeinv1.Preference{Kind: skeinv1.NotificationKind(p.Kind), InApp: p.InApp, Email: p.Email})
	}
	return resp, nil
}

// UpdatePreferences implements the RPC.
func (s *Server) UpdatePreferences(ctx context.Context, req *skeinv1.UpdatePreferencesRequest) (*skeinv1.UpdatePreferencesResponse, error) {
	user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	updates := make([]domain.Preference, 0, len(req.GetPreferences()))
	for _, p := range req.GetPreferences() {
		// A kind beyond uint8 can never be valid; clamp it to an unknown one
		// so validation rejects it instead of wrapping onto a real kind.
		kind := domain.KindUnspecified
		if k := p.GetKind(); k > 0 && k < 256 {
			kind = domain.Kind(k)
		} else if k >= 256 {
			kind = 255
		}
		updates = append(updates, domain.Preference{Kind: kind, InApp: p.GetInApp(), Email: p.GetEmail()})
	}
	if err := s.inbox.UpdatePreferences(ctx, user, updates); err != nil {
		return nil, s.toStatus(ctx, err)
	}
	return &skeinv1.UpdatePreferencesResponse{}, nil
}

// Subscribe implements the streaming RPC.
func (s *Server) Subscribe(_ *skeinv1.SubscribeRequest, stream grpc.ServerStreamingServer[skeinv1.SubscribeResponse]) error {
	ctx := stream.Context()
	user, err := caller(ctx)
	if err != nil {
		return err
	}
	sub, err := s.hub.Subscribe(ctx, user)
	if err != nil {
		s.logger.ErrorContext(ctx, "subscribe failed", slog.String("error", err.Error()))
		return status.Error(codes.Unavailable, "live notifications are unavailable; retry")
	}
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case <-sub.Done():
			if errors.Is(sub.Err(), app.ErrSlowSubscriber) {
				return status.Error(codes.ResourceExhausted, "subscriber fell behind; resubscribe and reload the inbox")
			}
			return status.Error(codes.Unavailable, "subscription closed")
		case n := <-sub.Notifications():
			if err := stream.Send(&skeinv1.SubscribeResponse{Notification: ToProto(n)}); err != nil {
				return err
			}
		}
	}
}
