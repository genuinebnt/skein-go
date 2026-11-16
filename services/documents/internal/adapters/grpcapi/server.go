// Package grpcapi serves DocumentService (proto/skein/v1/documents.proto)
// over gRPC: it validates and converts requests, calls the use cases in
// package app, and maps domain errors to status codes with the details
// docs/api/README.md promises. The caller is the subject of the bearer
// token grpcx verified (or the delegated token of an agent run).
package grpcapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
	"google.golang.org/protobuf/types/known/timestamppb"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/libs/dp"
	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/services/documents/internal/app"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
)

// Server implements skeinv1.DocumentServiceServer.
type Server struct {
	skeinv1.UnimplementedDocumentServiceServer
	app    *app.Service
	logger *slog.Logger
}

// New returns a server for the use cases in svc.
func New(svc *app.Service, logger *slog.Logger) *Server {
	if logger == nil {
		logger = logging.Discard()
	}
	return &Server{app: svc, logger: logger}
}

func caller(ctx context.Context) (domain.Subject, error) {
	s, ok := auth.SubjectFromContext(ctx)
	if !ok || s.UserID == "" {
		return domain.Subject{}, status.Error(codes.Unauthenticated, "no verified caller")
	}
	return domain.Subject{UserID: s.UserID, AgentRunID: s.AgentRunID}, nil
}

// toStatus maps a use-case error to a gRPC status. Messages carry IDs and
// sizes, never note contents.
func (s *Server) toStatus(ctx context.Context, err error, resource string) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok && !isDomain(err) {
		return err
	}
	var code codes.Code
	var details []protoadapt.MessageV1
	switch {
	case errors.Is(err, domain.ErrInvalidArgument):
		code = codes.InvalidArgument
		details = append(details, &errdetails.BadRequest{FieldViolations: []*errdetails.BadRequest_FieldViolation{{Description: err.Error()}}})
	case errors.Is(err, domain.ErrNotFound):
		code = codes.NotFound
		details = append(details, &errdetails.ResourceInfo{ResourceType: "skein.v1.Note", ResourceName: resource})
	case errors.Is(err, domain.ErrPermissionDenied):
		code = codes.PermissionDenied
		details = append(details, &errdetails.ErrorInfo{Reason: "ACTION_NOT_ALLOWED", Domain: "documents.skein"})
	case errors.Is(err, domain.ErrFailedPrecondition):
		code = codes.FailedPrecondition
		details = append(details, &errdetails.PreconditionFailure{Violations: []*errdetails.PreconditionFailure_Violation{{Type: "STATE", Subject: resource, Description: err.Error()}}})
	case errors.Is(err, domain.ErrOutOfRange):
		code = codes.OutOfRange
	case errors.Is(err, domain.ErrAlreadyExists):
		code = codes.AlreadyExists
	case errors.Is(err, domain.ErrConflict):
		code = codes.Aborted
	case errors.Is(err, domain.ErrResourceExhausted):
		code = codes.ResourceExhausted
	case errors.Is(err, domain.ErrUnavailable):
		code = codes.Unavailable
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	default:
		s.logger.ErrorContext(ctx, "internal error", "error", err, "note_id", resource)
		return status.Error(codes.Internal, "internal error")
	}
	if requestID := logging.RequestID(ctx); requestID != "" {
		details = append(details, &errdetails.RequestInfo{RequestId: requestID})
	}
	st := status.New(code, err.Error())
	if withDetails, derr := st.WithDetails(details...); derr == nil {
		st = withDetails
	}
	return st.Err()
}

func isDomain(err error) bool {
	for _, d := range []error{domain.ErrInvalidArgument, domain.ErrNotFound, domain.ErrPermissionDenied, domain.ErrFailedPrecondition,
		domain.ErrOutOfRange, domain.ErrAlreadyExists, domain.ErrConflict, domain.ErrResourceExhausted, domain.ErrUnavailable} {
		if errors.Is(err, d) {
			return true
		}
	}
	return false
}

func toNote(n domain.Note) *skeinv1.Note {
	return &skeinv1.Note{
		Id: string(n.ID), WorkspaceId: string(n.WorkspaceID), Title: n.Title, CreatedBy: n.CreatedBy,
		CreatedAt: timestamppb.New(n.CreatedAt), UpdatedAt: timestamppb.New(n.UpdatedAt), Trashed: n.Trashed(),
	}
}

func toVersion(v crdt.VersionVector) *skeinv1.VersionVector {
	out := &skeinv1.VersionVector{}
	for _, r := range v.Replicas() {
		out.Entries = append(out.Entries, &skeinv1.VersionEntry{ReplicaId: uint64(r), Counter: v[r]})
	}
	return out
}

func fromVersion(v *skeinv1.VersionVector) (crdt.VersionVector, error) {
	out := crdt.VersionVector{}
	for _, e := range v.GetEntries() {
		r := crdt.ReplicaID(e.GetReplicaId())
		if r == 0 {
			return nil, status.Error(codes.InvalidArgument, "version entries need a non-zero replica id")
		}
		if _, dup := out[r]; dup {
			return nil, status.Errorf(codes.InvalidArgument, "replica %d appears twice in the version", r)
		}
		if e.GetCounter() > 0 {
			out[r] = e.GetCounter()
		}
	}
	return out, nil
}

func noteID(s string) (domain.NoteID, error) {
	id, err := domain.ParseNoteID(s)
	if err != nil {
		return "", status.Error(codes.InvalidArgument, err.Error())
	}
	return id, nil
}

// CreateNote implements skeinv1.DocumentServiceServer.
func (s *Server) CreateNote(ctx context.Context, req *skeinv1.CreateNoteRequest) (*skeinv1.CreateNoteResponse, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ws, err := domain.ParseWorkspaceID(req.GetWorkspaceId())
	if err != nil {
		return nil, s.toStatus(ctx, err, "")
	}
	n, err := s.app.CreateNote(ctx, who, ws, req.GetTitle(), req.GetMarkdown())
	if err != nil {
		return nil, s.toStatus(ctx, err, "")
	}
	return &skeinv1.CreateNoteResponse{Note: toNote(n)}, nil
}

// GetNote implements skeinv1.DocumentServiceServer.
func (s *Server) GetNote(ctx context.Context, req *skeinv1.GetNoteRequest) (*skeinv1.GetNoteResponse, error) {
	who, id, err := s.target(ctx, req.GetNoteId())
	if err != nil {
		return nil, err
	}
	n, md, vv, err := s.app.GetNote(ctx, who, id, req.GetIncludeMarkdown())
	if err != nil {
		return nil, s.toStatus(ctx, err, string(id))
	}
	resp := &skeinv1.GetNoteResponse{Note: toNote(n), Markdown: md}
	if req.GetIncludeMarkdown() {
		resp.Version = toVersion(vv)
	}
	return resp, nil
}

func (s *Server) target(ctx context.Context, rawID string) (domain.Subject, domain.NoteID, error) {
	who, err := caller(ctx)
	if err != nil {
		return who, "", err
	}
	id, err := noteID(rawID)
	return who, id, err
}

// ListNotes implements skeinv1.DocumentServiceServer.
func (s *Server) ListNotes(ctx context.Context, req *skeinv1.ListNotesRequest) (*skeinv1.ListNotesResponse, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ws, err := domain.ParseWorkspaceID(req.GetWorkspaceId())
	if err != nil {
		return nil, s.toStatus(ctx, err, "")
	}
	notes, next, err := s.app.ListNotes(ctx, who, ws, req.GetPage().GetPageSize(), req.GetPage().GetPageToken(), req.GetIncludeTrashed())
	if err != nil {
		return nil, s.toStatus(ctx, err, "")
	}
	resp := &skeinv1.ListNotesResponse{Page: &skeinv1.PageResponse{NextPageToken: next}}
	for _, n := range notes {
		resp.Notes = append(resp.Notes, toNote(n))
	}
	return resp, nil
}

// DeleteNote implements skeinv1.DocumentServiceServer.
func (s *Server) DeleteNote(ctx context.Context, req *skeinv1.DeleteNoteRequest) (*skeinv1.DeleteNoteResponse, error) {
	who, id, err := s.target(ctx, req.GetNoteId())
	if err != nil {
		return nil, err
	}
	return &skeinv1.DeleteNoteResponse{}, s.toStatus(ctx, s.app.DeleteNote(ctx, who, id), string(id))
}

// RestoreNote implements skeinv1.DocumentServiceServer.
func (s *Server) RestoreNote(ctx context.Context, req *skeinv1.RestoreNoteRequest) (*skeinv1.RestoreNoteResponse, error) {
	who, id, err := s.target(ctx, req.GetNoteId())
	if err != nil {
		return nil, err
	}
	return &skeinv1.RestoreNoteResponse{}, s.toStatus(ctx, s.app.RestoreNote(ctx, who, id), string(id))
}

// RenameNote implements skeinv1.DocumentServiceServer.
func (s *Server) RenameNote(ctx context.Context, req *skeinv1.RenameNoteRequest) (*skeinv1.RenameNoteResponse, error) {
	who, id, err := s.target(ctx, req.GetNoteId())
	if err != nil {
		return nil, err
	}
	return &skeinv1.RenameNoteResponse{}, s.toStatus(ctx, s.app.RenameNote(ctx, who, id, req.GetTitle()), string(id))
}

// ApplyOps implements skeinv1.DocumentServiceServer.
func (s *Server) ApplyOps(ctx context.Context, req *skeinv1.ApplyOpsRequest) (*skeinv1.ApplyOpsResponse, error) {
	who, id, err := s.target(ctx, req.GetNoteId())
	if err != nil {
		return nil, err
	}
	vv, err := s.app.ApplyOps(ctx, who, id, crdt.ReplicaID(req.GetReplicaId()), req.GetOps())
	if err != nil {
		return nil, s.toStatus(ctx, err, string(id))
	}
	return &skeinv1.ApplyOpsResponse{Version: toVersion(vv)}, nil
}

// ListVersions implements skeinv1.DocumentServiceServer.
func (s *Server) ListVersions(ctx context.Context, req *skeinv1.ListVersionsRequest) (*skeinv1.ListVersionsResponse, error) {
	who, id, err := s.target(ctx, req.GetNoteId())
	if err != nil {
		return nil, err
	}
	versions, next, err := s.app.ListVersions(ctx, who, id, req.GetPage().GetPageSize(), req.GetPage().GetPageToken())
	if err != nil {
		return nil, s.toStatus(ctx, err, string(id))
	}
	resp := &skeinv1.ListVersionsResponse{Page: &skeinv1.PageResponse{NextPageToken: next}}
	for _, v := range versions {
		resp.Versions = append(resp.Versions, &skeinv1.NoteVersion{Version: toVersion(v.Vector), AuthorIds: slices.Clone(v.Authors), At: timestamppb.New(v.At)})
	}
	return resp, nil
}

// GetTextAt implements skeinv1.DocumentServiceServer.
func (s *Server) GetTextAt(ctx context.Context, req *skeinv1.GetTextAtRequest) (*skeinv1.GetTextAtResponse, error) {
	who, id, err := s.target(ctx, req.GetNoteId())
	if err != nil {
		return nil, err
	}
	v, err := fromVersion(req.GetVersion())
	if err != nil {
		return nil, err
	}
	md, err := s.app.GetTextAt(ctx, who, id, v)
	if err != nil {
		return nil, s.toStatus(ctx, err, string(id))
	}
	return &skeinv1.GetTextAtResponse{Markdown: md}, nil
}

var hunkKinds = map[dp.HunkKind]skeinv1.DiffHunk_Kind{
	dp.HunkEqual: skeinv1.DiffHunk_KIND_EQUAL, dp.HunkInsert: skeinv1.DiffHunk_KIND_INSERT, dp.HunkDelete: skeinv1.DiffHunk_KIND_DELETE,
}

// Diff implements skeinv1.DocumentServiceServer.
func (s *Server) Diff(ctx context.Context, req *skeinv1.DiffRequest) (*skeinv1.DiffResponse, error) {
	who, id, err := s.target(ctx, req.GetNoteId())
	if err != nil {
		return nil, err
	}
	from, err := fromVersion(req.GetFrom())
	if err != nil {
		return nil, err
	}
	to, err := fromVersion(req.GetTo())
	if err != nil {
		return nil, err
	}
	hunks, err := s.app.Diff(ctx, who, id, from, to)
	if err != nil {
		return nil, s.toStatus(ctx, err, string(id))
	}
	resp := &skeinv1.DiffResponse{}
	for _, h := range hunks {
		resp.Hunks = append(resp.Hunks, &skeinv1.DiffHunk{Kind: hunkKinds[h.Kind], Text: h.Text})
	}
	return resp, nil
}

// Blame implements skeinv1.DocumentServiceServer.
func (s *Server) Blame(ctx context.Context, req *skeinv1.BlameRequest) (*skeinv1.BlameResponse, error) {
	who, id, err := s.target(ctx, req.GetNoteId())
	if err != nil {
		return nil, err
	}
	spans, err := s.app.Blame(ctx, who, id)
	if err != nil {
		return nil, s.toStatus(ctx, err, string(id))
	}
	resp := &skeinv1.BlameResponse{}
	for _, sp := range spans {
		resp.Spans = append(resp.Spans, &skeinv1.BlameSpan{Start: uint32(sp.Start), End: uint32(sp.End), UserId: sp.UserID, AgentRunId: sp.AgentRunID, At: timestamppb.New(sp.At)})
	}
	return resp, nil
}

// Undo implements skeinv1.DocumentServiceServer.
func (s *Server) Undo(ctx context.Context, req *skeinv1.UndoRequest) (*skeinv1.UndoResponse, error) {
	who, id, err := s.target(ctx, req.GetNoteId())
	if err != nil {
		return nil, err
	}
	ops, err := s.app.Undo(ctx, who, id, crdt.ReplicaID(req.GetReplicaId()))
	if err != nil {
		return nil, s.toStatus(ctx, err, string(id))
	}
	return &skeinv1.UndoResponse{Ops: ops}, nil
}

// Redo implements skeinv1.DocumentServiceServer.
func (s *Server) Redo(ctx context.Context, req *skeinv1.RedoRequest) (*skeinv1.RedoResponse, error) {
	who, id, err := s.target(ctx, req.GetNoteId())
	if err != nil {
		return nil, err
	}
	ops, err := s.app.Redo(ctx, who, id, crdt.ReplicaID(req.GetReplicaId()))
	if err != nil {
		return nil, s.toStatus(ctx, err, string(id))
	}
	return &skeinv1.RedoResponse{Ops: ops}, nil
}

// Sync implements skeinv1.DocumentServiceServer: the first frame must be a
// hello; then one goroutine reads the client's frames and another sends
// the session's frames, because a gRPC stream allows one sender and one
// receiver at a time. The stream ends when the client closes it, the
// session is closed (revocation, a slow client, the note trashed), or the
// server shuts down.
func (s *Server) Sync(stream skeinv1.DocumentService_SyncServer) error {
	ctx := stream.Context()
	who, err := caller(ctx)
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "the first sync frame must be a hello")
	}
	id, err := noteID(hello.GetNoteId())
	if err != nil {
		return err
	}
	v, err := fromVersion(hello.GetVersion())
	if err != nil {
		return err
	}
	sess, err := s.app.OpenSession(ctx, who, app.Hello{Note: id, Replica: crdt.ReplicaID(hello.GetReplicaId()), Version: v})
	if err != nil {
		return s.toStatus(ctx, err, string(id))
	}
	defer sess.Close(context.WithoutCancel(ctx))

	sendDone := make(chan error, 1)
	go func() { sendDone <- s.sendFrames(ctx, stream, sess) }()
	recvDone := make(chan error, 1)
	go func() { recvDone <- s.receiveFrames(ctx, stream, sess) }()
	select {
	case err := <-recvDone:
		if errors.Is(err, io.EOF) {
			return nil
		}
		return s.toStatus(ctx, err, string(id))
	case err := <-sendDone:
		return s.toStatus(ctx, err, string(id))
	}
}

func (s *Server) sendFrames(ctx context.Context, stream skeinv1.DocumentService_SyncServer, sess *app.Session) error {
	send := func(f app.Frame) error { return stream.Send(toResponse(f)) }
	for {
		select {
		case f := <-sess.Frames():
			if err := send(f); err != nil {
				return err
			}
		case <-sess.Done():
			// Deliver what was queued (a rejection, the last ops) before
			// the stream ends with the reason.
			for {
				select {
				case f := <-sess.Frames():
					if err := send(f); err != nil {
						return err
					}
				default:
					return sess.Err()
				}
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Server) receiveFrames(ctx context.Context, stream skeinv1.DocumentService_SyncServer, sess *app.Session) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		switch m := req.GetMessage().(type) {
		case *skeinv1.SyncRequest_Ops:
			if err := sess.Submit(ctx, m.Ops); err != nil {
				return err
			}
		case *skeinv1.SyncRequest_Presence:
			if err := sess.SetPresence(ctx, m.Presence.GetAnchor(), m.Presence.GetHead()); err != nil {
				return err
			}
		default:
			return status.Error(codes.InvalidArgument, "only ops and presence frames may follow the hello")
		}
	}
}

func toResponse(f app.Frame) *skeinv1.SyncResponse {
	switch f.Kind {
	case app.FramePresence:
		p := f.Presence
		return &skeinv1.SyncResponse{Message: &skeinv1.SyncResponse_Presence{Presence: &skeinv1.Presence{UserId: p.UserID, ReplicaId: uint64(p.Replica), Anchor: p.Anchor, Head: p.Head}}}
	case app.FrameRejected:
		return &skeinv1.SyncResponse{Message: &skeinv1.SyncResponse_Rejected{Rejected: &skeinv1.SyncRejected{Ops: f.Ops, Reason: f.Reason}}}
	default:
		return &skeinv1.SyncResponse{Message: &skeinv1.SyncResponse_Ops{Ops: f.Ops}}
	}
}
