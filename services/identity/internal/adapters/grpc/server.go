// Package grpc serves skein.v1.IdentityService: it translates requests to
// use-case calls and results back, and maps errors to statuses
// (errors.go). It holds no logic of its own beyond that translation.
//
// The caller is the subject of the verified access token that grpcx's auth
// interceptor put in the context. Sign-up, sign-in, OAuth, refresh and
// sign-out are public (PublicMethods); every other RPC needs a token, except
// that Authorize and ListReadableNotes may be made public by configuration
// for event consumers without a user token (see app.CheckInternalCaller).
//
// The client IP for sign-in rate limits is the first X-Forwarded-For entry
// (the gateway forwards it as x-forwarded-for metadata), else the peer
// address. Identity is reachable only from inside the cluster, so the
// header is set by the gateway, not by the browser.
package grpc

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/logging"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/domain"
)

// PublicMethods are the RPCs callable without an access token.
func PublicMethods(allowUnauthenticatedInternal bool) []string {
	m := []string{
		skeinv1.IdentityService_SignUp_FullMethodName,
		skeinv1.IdentityService_SignIn_FullMethodName,
		skeinv1.IdentityService_SignInWithOAuth_FullMethodName,
		skeinv1.IdentityService_Refresh_FullMethodName,
		skeinv1.IdentityService_SignOut_FullMethodName,
	}
	if allowUnauthenticatedInternal {
		m = append(m, skeinv1.IdentityService_Authorize_FullMethodName, skeinv1.IdentityService_ListReadableNotes_FullMethodName,
			// Event consumers resolve @handles and read names and
			// addresses; without these, notifications dead-letters every
			// mention event.
			skeinv1.IdentityService_GetUser_FullMethodName, skeinv1.IdentityService_ListMembers_FullMethodName)
	}
	return m
}

// Server implements skeinv1.IdentityServiceServer.
type Server struct {
	skeinv1.UnimplementedIdentityServiceServer
	svc *app.Service
	log *slog.Logger
}

// NewServer returns a server for the use cases in svc.
func NewServer(svc *app.Service, log *slog.Logger) *Server {
	if log == nil {
		log = logging.Discard()
	}
	return &Server{svc: svc, log: log}
}

// caller returns the authenticated subject, or nil.
func caller(ctx context.Context) *domain.Subject {
	s, ok := auth.SubjectFromContext(ctx)
	if !ok || s.UserID == "" {
		return nil
	}
	subj := domain.UserSubject(domain.UserID(s.UserID), domain.AgentRunID(s.AgentRunID))
	return &subj
}

// mustCaller returns the authenticated subject, or a zero subject that the
// use cases refuse as unauthenticated.
func mustCaller(ctx context.Context) domain.Subject {
	if c := caller(ctx); c != nil {
		return *c
	}
	return domain.Subject{}
}

func clientIP(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("x-forwarded-for"); len(v) > 0 {
			first, _, _ := strings.Cut(v[0], ",")
			if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
				return ip.String()
			}
		}
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		host, _, err := net.SplitHostPort(p.Addr.String())
		if err == nil {
			return host
		}
	}
	return ""
}

func ts(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }

func optionalTime(t *timestamppb.Timestamp) *time.Time {
	if t == nil {
		return nil
	}
	v := t.AsTime()
	return &v
}

func userProto(u domain.User) *skeinv1.User {
	return &skeinv1.User{Id: string(u.ID), Email: u.Email, DisplayName: u.DisplayName, CreatedAt: ts(u.CreatedAt)}
}

func sessionProto(s app.SessionTokens) *skeinv1.Session {
	return &skeinv1.Session{AccessToken: s.AccessToken, AccessExpiresAt: ts(s.AccessExpiresAt),
		RefreshToken: s.RefreshToken, RefreshExpiresAt: ts(s.RefreshExpiresAt)}
}

func (s *Server) fail(ctx context.Context, err error) error { return toStatus(ctx, s.log, err) }

// SignUp implements the RPC.
func (s *Server) SignUp(ctx context.Context, req *skeinv1.SignUpRequest) (*skeinv1.SignUpResponse, error) {
	u, sess, err := s.svc.SignUp(ctx, req.GetEmail(), req.GetPassword(), req.GetDisplayName())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.SignUpResponse{User: userProto(u), Session: sessionProto(sess)}, nil
}

// SignIn implements the RPC.
func (s *Server) SignIn(ctx context.Context, req *skeinv1.SignInRequest) (*skeinv1.SignInResponse, error) {
	u, sess, err := s.svc.SignIn(ctx, req.GetEmail(), req.GetPassword(), clientIP(ctx))
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.SignInResponse{User: userProto(u), Session: sessionProto(sess)}, nil
}

// SignInWithOAuth implements the RPC.
func (s *Server) SignInWithOAuth(ctx context.Context, req *skeinv1.SignInWithOAuthRequest) (*skeinv1.SignInWithOAuthResponse, error) {
	res, err := s.svc.SignInWithOAuth(ctx, app.OAuthProviderKind(req.GetProvider()), req.GetCode(), req.GetCodeVerifier(), req.GetRedirectUri())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.SignInWithOAuthResponse{User: userProto(res.User), Session: sessionProto(res.Tokens), Created: res.Created}, nil
}

// Refresh implements the RPC.
func (s *Server) Refresh(ctx context.Context, req *skeinv1.RefreshRequest) (*skeinv1.RefreshResponse, error) {
	sess, err := s.svc.Refresh(ctx, req.GetRefreshToken())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.RefreshResponse{Session: sessionProto(sess)}, nil
}

// SignOut implements the RPC.
func (s *Server) SignOut(ctx context.Context, req *skeinv1.SignOutRequest) (*skeinv1.SignOutResponse, error) {
	if err := s.svc.SignOut(ctx, req.GetRefreshToken()); err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.SignOutResponse{}, nil
}

// GetUser implements the RPC.
func (s *Server) GetUser(ctx context.Context, req *skeinv1.GetUserRequest) (*skeinv1.GetUserResponse, error) {
	id, err := domain.ParseUserID("user_id", req.GetUserId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	var u domain.User
	if c := caller(ctx); c == nil {
		u, err = s.svc.GetUserInternal(ctx, id)
	} else {
		u, err = s.svc.GetUser(ctx, *c, id)
	}
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.GetUserResponse{User: userProto(u)}, nil
}

// CreateWorkspace implements the RPC.
func (s *Server) CreateWorkspace(ctx context.Context, req *skeinv1.CreateWorkspaceRequest) (*skeinv1.CreateWorkspaceResponse, error) {
	id, err := s.svc.CreateWorkspace(ctx, mustCaller(ctx), req.GetName())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.CreateWorkspaceResponse{WorkspaceId: string(id)}, nil
}

// ListMembers implements the RPC.
func (s *Server) ListMembers(ctx context.Context, req *skeinv1.ListMembersRequest) (*skeinv1.ListMembersResponse, error) {
	ws, err := domain.ParseWorkspaceID("workspace_id", req.GetWorkspaceId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	var page app.MemberPage
	if c := caller(ctx); c == nil {
		page, err = s.svc.ListMembersInternal(ctx, ws, req.GetPage().GetPageSize(), req.GetPage().GetPageToken())
	} else {
		page, err = s.svc.ListMembers(ctx, *c, ws, req.GetPage().GetPageSize(), req.GetPage().GetPageToken())
	}
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	out := &skeinv1.ListMembersResponse{Page: &skeinv1.PageResponse{NextPageToken: page.NextPageToken}}
	for _, m := range page.Members {
		out.Members = append(out.Members, &skeinv1.Member{User: userProto(m.User), Role: skeinv1.WorkspaceRole(m.Role)})
	}
	return out, nil
}

func parseMemberRequest(wsID, userID string) (domain.WorkspaceID, domain.UserID, error) {
	ws, err := domain.ParseWorkspaceID("workspace_id", wsID)
	if err != nil {
		return "", "", err
	}
	user, err := domain.ParseUserID("user_id", userID)
	return ws, user, err
}

// SetWorkspaceRole implements the RPC.
func (s *Server) SetWorkspaceRole(ctx context.Context, req *skeinv1.SetWorkspaceRoleRequest) (*skeinv1.SetWorkspaceRoleResponse, error) {
	ws, user, err := parseMemberRequest(req.GetWorkspaceId(), req.GetUserId())
	if err == nil {
		err = s.svc.SetWorkspaceRole(ctx, mustCaller(ctx), ws, user, domain.WorkspaceRole(req.GetRole()))
	}
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.SetWorkspaceRoleResponse{}, nil
}

// RemoveMember implements the RPC.
func (s *Server) RemoveMember(ctx context.Context, req *skeinv1.RemoveMemberRequest) (*skeinv1.RemoveMemberResponse, error) {
	ws, user, err := parseMemberRequest(req.GetWorkspaceId(), req.GetUserId())
	if err == nil {
		err = s.svc.RemoveMember(ctx, mustCaller(ctx), ws, user)
	}
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.RemoveMemberResponse{}, nil
}

// SetNoteRole implements the RPC.
func (s *Server) SetNoteRole(ctx context.Context, req *skeinv1.SetNoteRoleRequest) (*skeinv1.SetNoteRoleResponse, error) {
	note, err := domain.ParseNoteID("note_id", req.GetNoteId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	user, err := domain.ParseUserID("user_id", req.GetUserId())
	if err == nil {
		err = s.svc.SetNoteRole(ctx, mustCaller(ctx), note, user, domain.NoteRole(req.GetRole()))
	}
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.SetNoteRoleResponse{}, nil
}

// CreateShareLink implements the RPC.
func (s *Server) CreateShareLink(ctx context.Context, req *skeinv1.CreateShareLinkRequest) (*skeinv1.CreateShareLinkResponse, error) {
	note, err := domain.ParseNoteID("note_id", req.GetNoteId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	id, token, err := s.svc.CreateShareLink(ctx, mustCaller(ctx), note, domain.NoteRole(req.GetRole()), optionalTime(req.GetExpiresAt()))
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.CreateShareLinkResponse{LinkId: string(id), Token: token}, nil
}

// RevokeShareLink implements the RPC.
func (s *Server) RevokeShareLink(ctx context.Context, req *skeinv1.RevokeShareLinkRequest) (*skeinv1.RevokeShareLinkResponse, error) {
	id, err := domain.ParseShareLinkID("link_id", req.GetLinkId())
	if err == nil {
		err = s.svc.RevokeShareLink(ctx, mustCaller(ctx), id)
	}
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.RevokeShareLinkResponse{}, nil
}

// internalSubject validates a request subject and checks the caller may
// ask about it.
func (s *Server) internalSubject(ctx context.Context, subj *skeinv1.Subject) (domain.Subject, error) {
	subject, err := app.ValidateSubject(subj.GetUserId(), subj.GetAgentRunId())
	if err != nil {
		return domain.Subject{}, err
	}
	return subject, s.svc.CheckInternalCaller(caller(ctx), subject)
}

// Authorize implements the RPC.
func (s *Server) Authorize(ctx context.Context, req *skeinv1.AuthorizeRequest) (*skeinv1.AuthorizeResponse, error) {
	subject, err := s.internalSubject(ctx, req.GetSubject())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	checks := make([]domain.Check, len(req.GetChecks()))
	for i, c := range req.GetChecks() {
		checks[i] = domain.Check{Action: domain.Action(c.GetAction()), ResourceID: c.GetResourceId()}
	}
	decisions, err := s.svc.Authorize(ctx, subject, checks)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	out := &skeinv1.AuthorizeResponse{Allowed: make([]bool, len(decisions))}
	for i, d := range decisions {
		out.Allowed[i] = d.Allowed
	}
	return out, nil
}

// ListReadableNotes implements the RPC.
func (s *Server) ListReadableNotes(ctx context.Context, req *skeinv1.ListReadableNotesRequest) (*skeinv1.ListReadableNotesResponse, error) {
	subject, err := s.internalSubject(ctx, req.GetSubject())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	ws, err := domain.ParseWorkspaceID("workspace_id", req.GetWorkspaceId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	ids, err := s.svc.ListReadableNotes(ctx, subject, ws)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	out := &skeinv1.ListReadableNotesResponse{NoteIds: make([]string, len(ids))}
	for i, id := range ids {
		out.NoteIds[i] = string(id)
	}
	return out, nil
}

// IssueDelegatedToken implements the RPC.
func (s *Server) IssueDelegatedToken(ctx context.Context, req *skeinv1.IssueDelegatedTokenRequest) (*skeinv1.IssueDelegatedTokenResponse, error) {
	user, err := domain.ParseUserID("user_id", req.GetUserId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	run, err := domain.ParseAgentRunID("agent_run_id", req.GetAgentRunId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	token, _, err := s.svc.IssueDelegatedToken(ctx, mustCaller(ctx), user, run, optionalTime(req.GetExpiresAt()))
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.IssueDelegatedTokenResponse{AccessToken: token}, nil
}

// RegisterNote implements the RPC.
func (s *Server) RegisterNote(ctx context.Context, req *skeinv1.RegisterNoteRequest) (*skeinv1.RegisterNoteResponse, error) {
	note, err := domain.ParseNoteID("note_id", req.GetNoteId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	ws, err := domain.ParseWorkspaceID("workspace_id", req.GetWorkspaceId())
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	if err := s.svc.RegisterNote(ctx, mustCaller(ctx), note, ws); err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.RegisterNoteResponse{}, nil
}

// RegisterReplica implements the RPC.
func (s *Server) RegisterReplica(ctx context.Context, req *skeinv1.RegisterReplicaRequest) (*skeinv1.RegisterReplicaResponse, error) {
	id, err := domain.ParseReplicaID("replica_id", req.GetReplicaId())
	if err == nil {
		err = s.svc.RegisterReplica(ctx, mustCaller(ctx), id)
	}
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	return &skeinv1.RegisterReplicaResponse{}, nil
}
