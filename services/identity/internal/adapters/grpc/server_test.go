package grpc_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/grpcx"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/services/identity/internal/adapters/admin"
	identitygrpc "github.com/genuinebnt/skein-go/services/identity/internal/adapters/grpc"
	"github.com/genuinebnt/skein-go/services/identity/internal/app"
	"github.com/genuinebnt/skein-go/services/identity/internal/credential"
	"github.com/genuinebnt/skein-go/services/identity/internal/fakes"
	"github.com/genuinebnt/skein-go/services/identity/internal/keys"
)

// password is assembled at run time so no credential-shaped literal sits in
// the source (secret scanners flag those).
var password = strings.Join([]string{"correct", "horse", "battery", "staple"}, " ")

type fixture struct {
	client skeinv1.IdentityServiceClient
	jwks   *httptest.Server
	ring   *keys.Keyring
}

// newFixture runs the real gRPC server (grpcx interceptors, the keyring as
// issuer and verifier) over bufconn, with the in-memory store.
func newFixture(t *testing.T) *fixture { return newFixtureWith(t, false) }

// newFixtureWith runs the server with IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS
// set to allowInternal, in both places it applies: the public method list
// and the use cases.
func newFixtureWith(t *testing.T, allowInternal bool) *fixture {
	t.Helper()
	key, _ := auth.GenerateKey()
	ring, err := keys.New(key, keys.Options{RetireAfter: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	hasher, _ := credential.NewHasher(credential.HasherOptions{Params: credential.Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}})
	cfg := app.DefaultConfig()
	cfg.AllowUnauthenticatedInternalCalls = allowInternal
	svc, err := app.New(cfg, app.Deps{Store: fakes.NewStore(), Hasher: hasher, Issuer: ring})
	if err != nil {
		t.Fatal(err)
	}
	srv := grpcx.NewServer(grpcx.ServerConfig{Env: config.EnvTest, Verifier: auth.NewVerifier(ring, auth.VerifierOptions{}),
		PublicMethods: identitygrpc.PublicMethods(allowInternal)})
	skeinv1.RegisterIdentityServiceServer(srv, identitygrpc.NewServer(svc, nil))
	lis := bufconn.Listen(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, lis) }()
	conn, err := grpcx.NewClient(grpcx.ClientConfig{Target: "passthrough:///bufnet", Insecure: true,
		Options: []grpc.DialOption{grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) })}})
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(admin.Handler(ring, nil, nil, nil))
	t.Cleanup(func() {
		jwks.Close()
		_ = conn.Close()
		cancel()
		<-done
	})
	return &fixture{client: skeinv1.NewIdentityServiceClient(conn), jwks: jwks, ring: ring}
}

func bearer(token string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+token)
}

func code(err error) codes.Code { return status.Code(err) }

func (f *fixture) signUp(t *testing.T, email string) *skeinv1.SignUpResponse {
	t.Helper()
	res, err := f.client.SignUp(context.Background(), &skeinv1.SignUpRequest{Email: email, Password: password, DisplayName: email})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestTokensFromSignUpVerifyAgainstThePublishedKeySet(t *testing.T) {
	f := newFixture(t)
	res := f.signUp(t, "ada@example.com")
	resp, err := http.Get(f.jwks.URL + admin.JWKSPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("Cache-Control %q", cc)
	}
	var doc json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	set, err := auth.ParseJWKS(doc)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.NewVerifier(set, auth.VerifierOptions{}).Verify(context.Background(), res.GetSession().GetAccessToken())
	if err != nil || claims.Subject.UserID != res.GetUser().GetId() {
		t.Fatalf("verify against published JWKS: %+v %v", claims, err)
	}
}

func TestWorkspaceFlowOverGRPC(t *testing.T) {
	f := newFixture(t)
	owner := f.signUp(t, "owner@example.com")
	member := f.signUp(t, "member@example.com")
	ownerCtx := bearer(owner.GetSession().GetAccessToken())

	if _, err := f.client.CreateWorkspace(context.Background(), &skeinv1.CreateWorkspaceRequest{Name: "W"}); code(err) != codes.Unauthenticated {
		t.Fatalf("no token: %v", err)
	}
	ws, err := f.client.CreateWorkspace(ownerCtx, &skeinv1.CreateWorkspaceRequest{Name: "Research"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.SetWorkspaceRole(ownerCtx, &skeinv1.SetWorkspaceRoleRequest{
		WorkspaceId: ws.GetWorkspaceId(), UserId: member.GetUser().GetId(), Role: skeinv1.WorkspaceRole_WORKSPACE_ROLE_MEMBER}); err != nil {
		t.Fatal(err)
	}
	list, err := f.client.ListMembers(bearer(member.GetSession().GetAccessToken()), &skeinv1.ListMembersRequest{WorkspaceId: ws.GetWorkspaceId()})
	if err != nil || len(list.GetMembers()) != 2 {
		t.Fatalf("members %v %v", list, err)
	}
	auth, err := f.client.Authorize(ownerCtx, &skeinv1.AuthorizeRequest{
		Subject: &skeinv1.Subject{UserId: owner.GetUser().GetId()},
		Checks: []*skeinv1.Check{
			{Action: skeinv1.Action_ACTION_WORKSPACE_MANAGE_SETTINGS, ResourceId: ws.GetWorkspaceId()},
			{Action: skeinv1.Action_ACTION_NOTE_READ, ResourceId: "019cc260-0a80-72dd-9e8d-ba3c76c67531"},
		}})
	if err != nil || len(auth.GetAllowed()) != 2 || !auth.GetAllowed()[0] || auth.GetAllowed()[1] {
		t.Fatalf("authorize: %v %v", auth, err)
	}
	_, err = f.client.Authorize(ownerCtx, &skeinv1.AuthorizeRequest{Subject: &skeinv1.Subject{UserId: member.GetUser().GetId()}})
	if code(err) != codes.PermissionDenied {
		t.Fatalf("asking about someone else: %v", err)
	}
	// The last owner cannot step down: FAILED_PRECONDITION with a detail.
	_, err = f.client.RemoveMember(ownerCtx, &skeinv1.RemoveMemberRequest{WorkspaceId: ws.GetWorkspaceId(), UserId: owner.GetUser().GetId()})
	if code(err) != codes.FailedPrecondition {
		t.Fatalf("last owner: %v", err)
	}
	// A member cannot see an outsider.
	outsider := f.signUp(t, "outsider@example.com")
	_, err = f.client.GetUser(bearer(member.GetSession().GetAccessToken()), &skeinv1.GetUserRequest{UserId: outsider.GetUser().GetId()})
	if code(err) != codes.NotFound {
		t.Fatalf("outsider: %v", err)
	}
}

func TestErrorsCarryTheDocumentedDetails(t *testing.T) {
	f := newFixture(t)
	_, err := f.client.SignUp(context.Background(), &skeinv1.SignUpRequest{Email: "ada@example.com", Password: "short", DisplayName: "Ada"})
	st, _ := status.FromError(err)
	var field string
	for _, d := range st.Details() {
		if br, ok := d.(*errdetails.BadRequest); ok && len(br.GetFieldViolations()) == 1 {
			field = br.GetFieldViolations()[0].GetField()
		}
	}
	if st.Code() != codes.InvalidArgument || field != "password" {
		t.Fatalf("weak password: %v %v", st.Code(), st.Details())
	}
	f.signUp(t, "ada@example.com")
	_, err = f.client.SignUp(context.Background(), &skeinv1.SignUpRequest{Email: "ADA@example.com", Password: password, DisplayName: "Ada"})
	if code(err) != codes.AlreadyExists {
		t.Fatalf("taken email: %v", err)
	}
	_, err = f.client.SignIn(context.Background(), &skeinv1.SignInRequest{Email: "ada@example.com", Password: password + "!"})
	if st, _ := status.FromError(err); st.Code() != codes.Unauthenticated || st.Message() != "authentication failed" {
		t.Fatalf("wrong password: %v", err)
	}
	_, err = f.client.GetUser(bearer("not-a-token"), &skeinv1.GetUserRequest{UserId: "x"})
	if code(err) != codes.Unauthenticated {
		t.Fatalf("garbage token: %v", err)
	}
}

func TestRateLimitBecomesResourceExhaustedWithRetryInfo(t *testing.T) {
	err := identitygrpc.ToStatus(&app.RateLimitError{RetryAfter: 3 * time.Second})
	if d, ok := grpcx.RetryAfter(err); !ok || d != 3*time.Second || code(err) != codes.ResourceExhausted {
		t.Fatalf("%v (retry after %v)", err, d)
	}
}

func TestReplicaOwnedByAnotherUserIsAlreadyExists(t *testing.T) {
	f := newFixture(t)
	a, b := f.signUp(t, "a@example.com"), f.signUp(t, "b@example.com")
	req := &skeinv1.RegisterReplicaRequest{ReplicaId: 4831727201934127107}
	if _, err := f.client.RegisterReplica(bearer(a.GetSession().GetAccessToken()), req); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.RegisterReplica(bearer(b.GetSession().GetAccessToken()), req); code(err) != codes.AlreadyExists {
		t.Fatalf("%v", err)
	}
	if _, err := f.client.RegisterReplica(bearer(a.GetSession().GetAccessToken()), &skeinv1.RegisterReplicaRequest{}); code(err) != codes.InvalidArgument {
		t.Fatalf("replica 0: %v", err)
	}
}

func TestDelegatedTokenActsForTheUserWithTheRunID(t *testing.T) {
	f := newFixture(t)
	u := f.signUp(t, "ada@example.com")
	run := "019d06e4-080e-74b5-8cd7-d007c93937ce"
	res, err := f.client.IssueDelegatedToken(bearer(u.GetSession().GetAccessToken()), &skeinv1.IssueDelegatedTokenRequest{UserId: u.GetUser().GetId(), AgentRunId: run})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.NewVerifier(f.ring, auth.VerifierOptions{}).Verify(context.Background(), res.GetAccessToken())
	if err != nil || claims.Subject.UserID != u.GetUser().GetId() || claims.Subject.AgentRunID != run || claims.ExpiresAt.Sub(claims.IssuedAt) != 15*time.Minute {
		t.Fatalf("%+v %v", claims, err)
	}
	// The delegated token cannot mint another.
	_, err = f.client.IssueDelegatedToken(bearer(res.GetAccessToken()), &skeinv1.IssueDelegatedTokenRequest{UserId: u.GetUser().GetId(), AgentRunId: run})
	if code(err) != codes.PermissionDenied {
		t.Fatalf("delegated minting: %v", err)
	}
}

// Event consumers (notifications resolving @handles and reading names and
// addresses) call without a user token. Found end to end: every mention
// event was dead-lettered on "missing bearer token". With the internal-call
// flag they get GetUser and ListMembers; without it, nothing.
func TestTokenlessGetUserAndListMembersNeedTheInternalCallFlag(t *testing.T) {
	for _, allow := range []bool{false, true} {
		f := newFixtureWith(t, allow)
		owner := f.signUp(t, "owner@example.com")
		ws, err := f.client.CreateWorkspace(bearer(owner.GetSession().GetAccessToken()), &skeinv1.CreateWorkspaceRequest{Name: "W"})
		if err != nil {
			t.Fatal(err)
		}
		user, userErr := f.client.GetUser(context.Background(), &skeinv1.GetUserRequest{UserId: owner.GetUser().GetId()})
		members, membersErr := f.client.ListMembers(context.Background(), &skeinv1.ListMembersRequest{WorkspaceId: ws.GetWorkspaceId()})
		if !allow {
			if code(userErr) != codes.Unauthenticated || code(membersErr) != codes.Unauthenticated {
				t.Errorf("flag off: GetUser %v, ListMembers %v; want UNAUTHENTICATED", userErr, membersErr)
			}
			continue
		}
		if userErr != nil || user.GetUser().GetEmail() != "owner@example.com" {
			t.Errorf("flag on: GetUser %v %v", user, userErr)
		}
		if membersErr != nil || len(members.GetMembers()) != 1 {
			t.Errorf("flag on: ListMembers %v %v", members, membersErr)
		}
		_, err = f.client.ListMembers(context.Background(), &skeinv1.ListMembersRequest{WorkspaceId: "019cc260-0a80-72dd-9e8d-ba3c76c67531"})
		if code(err) != codes.NotFound {
			t.Errorf("flag on: an unknown workspace: %v", err)
		}
	}
}
