package identity

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/ports"
)

type stub struct {
	skeinv1.IdentityServiceClient
	got      *skeinv1.AuthorizeRequest
	register error
}

func (s *stub) Authorize(_ context.Context, req *skeinv1.AuthorizeRequest, _ ...grpc.CallOption) (*skeinv1.AuthorizeResponse, error) {
	s.got = req
	return &skeinv1.AuthorizeResponse{Allowed: []bool{true, false}}, nil
}

func (s *stub) RegisterReplica(context.Context, *skeinv1.RegisterReplicaRequest, ...grpc.CallOption) (*skeinv1.RegisterReplicaResponse, error) {
	return &skeinv1.RegisterReplicaResponse{}, s.register
}

func TestAuthorizeMapsSubjectAndActions(t *testing.T) {
	s := &stub{}
	got, err := New(s).Authorize(context.Background(), domain.Subject{UserID: "u", AgentRunID: "r"},
		[]ports.Check{{Action: ports.ActionRead, Resource: "n"}, {Action: ports.ActionEdit, Resource: "n"}})
	if err != nil || !got[0] || got[1] {
		t.Fatalf("%v %v", got, err)
	}
	if s.got.GetSubject().GetAgentRunId() != "r" || s.got.GetChecks()[1].GetAction() != skeinv1.Action_ACTION_NOTE_EDIT {
		t.Fatalf("request %v", s.got)
	}
}

func TestReplicaOwnedBySomeoneElseIsPermissionDenied(t *testing.T) {
	s := &stub{register: status.Error(codes.AlreadyExists, "taken")}
	if err := New(s).EnsureReplica(context.Background(), domain.Subject{}, 1<<40); !errors.Is(err, domain.ErrPermissionDenied) {
		t.Fatalf("err %v", err)
	}
	s.register = status.Error(codes.Unavailable, "down")
	if err := New(s).EnsureReplica(context.Background(), domain.Subject{}, 1<<40); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("err %v", err)
	}
}
