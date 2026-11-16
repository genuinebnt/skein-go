package identity

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
)

type fakeIdentity struct {
	allowed     map[string]bool // user/action/resource
	users       map[string]*skeinv1.User
	members     []*skeinv1.Member
	pageSize    int
	authzCalls  int
	checks      int
	memberCalls int
	tokens      []string
}

func (f *fakeIdentity) token(ctx context.Context) {
	md, _ := metadata.FromOutgoingContext(ctx)
	f.tokens = append(f.tokens, md.Get("authorization")...)
}

func (f *fakeIdentity) Authorize(ctx context.Context, in *skeinv1.AuthorizeRequest, _ ...grpc.CallOption) (*skeinv1.AuthorizeResponse, error) {
	f.token(ctx)
	f.authzCalls++
	resp := &skeinv1.AuthorizeResponse{}
	for _, c := range in.GetChecks() {
		f.checks++
		resp.Allowed = append(resp.Allowed, f.allowed[fmt.Sprintf("%s/%d/%s", in.GetSubject().GetUserId(), c.GetAction(), c.GetResourceId())])
	}
	return resp, nil
}

func (f *fakeIdentity) GetUser(ctx context.Context, in *skeinv1.GetUserRequest, _ ...grpc.CallOption) (*skeinv1.GetUserResponse, error) {
	f.token(ctx)
	u, ok := f.users[in.GetUserId()]
	if !ok {
		return nil, status.Error(codes.NotFound, "no such user")
	}
	return &skeinv1.GetUserResponse{User: u}, nil
}

func (f *fakeIdentity) ListMembers(ctx context.Context, in *skeinv1.ListMembersRequest, _ ...grpc.CallOption) (*skeinv1.ListMembersResponse, error) {
	f.token(ctx)
	f.memberCalls++
	start := 0
	if t := in.GetPage().GetPageToken(); t != "" {
		fmt.Sscanf(t, "%d", &start)
	}
	end := min(len(f.members), start+f.pageSize)
	resp := &skeinv1.ListMembersResponse{Members: f.members[start:end], Page: &skeinv1.PageResponse{}}
	if end < len(f.members) {
		resp.Page.NextPageToken = fmt.Sprint(end)
	}
	return resp, nil
}

func newAdapter(t *testing.T, f *fakeIdentity, token string) *Identity {
	t.Helper()
	opts := Options{}
	if token != "" {
		opts.ServiceToken = config.NewSecret(token)
	}
	i, err := New(f, redisx.NewMemoryStore(time.Now), opts)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestActionNumbersMatchTheProto(t *testing.T) {
	want := map[app.Action]skeinv1.Action{
		app.ActionNoteRead: skeinv1.Action_ACTION_NOTE_READ, app.ActionNoteComment: skeinv1.Action_ACTION_NOTE_COMMENT,
		app.ActionNoteEdit: skeinv1.Action_ACTION_NOTE_EDIT, app.ActionNoteCreate: skeinv1.Action_ACTION_NOTE_CREATE,
	}
	for _, a := range CheckActions {
		if skeinv1.Action(a) != want[a] {
			t.Errorf("action %d is %s in the proto", a, skeinv1.Action(a))
		}
	}
}

func TestAuthorizeCachesAnswersAndAsksOnlyForMisses(t *testing.T) {
	f := &fakeIdentity{allowed: map[string]bool{"u1/1/n1": true}}
	i := newAdapter(t, f, "")
	ctx := context.Background()
	got, err := i.Authorize(ctx, "u1", []app.Check{{Action: app.ActionNoteRead, ResourceID: "n1"}, {Action: app.ActionNoteRead, ResourceID: "n2"}}, time.Time{})
	if err != nil || !got[0] || got[1] {
		t.Fatalf("Authorize = %v, %v", got, err)
	}
	got, _ = i.Authorize(ctx, "u1", []app.Check{{Action: app.ActionNoteRead, ResourceID: "n2"}, {Action: app.ActionNoteEdit, ResourceID: "n1"}, {Action: app.ActionNoteRead, ResourceID: "n1"}}, time.Time{})
	if got[0] || got[1] || !got[2] {
		t.Fatalf("second Authorize = %v", got)
	}
	if f.authzCalls != 2 || f.checks != 3 {
		t.Fatalf("%d calls with %d checks; want the second call to ask only for the one miss", f.authzCalls, f.checks)
	}
}

func TestAccessChangesDropTheUsersCachedAnswers(t *testing.T) {
	f := &fakeIdentity{allowed: map[string]bool{"u1/1/n1": true, "u2/1/n1": true}}
	i := newAdapter(t, f, "")
	ctx := context.Background()
	for _, u := range []string{"u1", "u2"} {
		if _, err := i.Authorize(ctx, u, []app.Check{{Action: app.ActionNoteRead, ResourceID: "n1"}}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	f.allowed["u1/1/n1"] = false
	if err := i.InvalidateUser(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	got, _ := i.Authorize(ctx, "u1", []app.Check{{Action: app.ActionNoteRead, ResourceID: "n1"}}, time.Time{})
	if got[0] {
		t.Fatal("revoked access still cached")
	}
	calls := f.authzCalls
	_, _ = i.Authorize(ctx, "u2", []app.Check{{Action: app.ActionNoteRead, ResourceID: "n1"}}, time.Time{})
	if f.authzCalls != calls {
		t.Fatal("invalidating u1 dropped u2's answers")
	}
}

func TestProfilesAreCachedAndUnknownUsersReported(t *testing.T) {
	f := &fakeIdentity{users: map[string]*skeinv1.User{"u1": {Id: "u1", Email: "ada@example.com", DisplayName: "Ada"}}}
	i := newAdapter(t, f, "")
	p, err := i.Profile(context.Background(), "u1")
	if err != nil || p.DisplayName != "Ada" || p.Email != "ada@example.com" {
		t.Fatalf("Profile = %+v, %v", p, err)
	}
	if _, err := i.Profile(context.Background(), "nobody"); !errors.Is(err, app.ErrUnknownUser) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestHandlesResolveFromEmailLocalPartsAcrossPages(t *testing.T) {
	f := &fakeIdentity{pageSize: 2}
	for n, email := range []string{"Ada@example.com", "grace@example.com", "kaito.varga@example.com", "bad-address"} {
		f.members = append(f.members, &skeinv1.Member{User: &skeinv1.User{Id: fmt.Sprintf("u%d", n), Email: email}})
	}
	i := newAdapter(t, f, "service-token")
	got, err := i.ResolveHandles(context.Background(), "ws", []string{"ada", "kaito.varga", "nobody"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["ada"] != "u0" || got["kaito.varga"] != "u2" {
		t.Fatalf("resolved %v", got)
	}
	calls := f.memberCalls
	_, _ = i.ResolveHandles(context.Background(), "ws", []string{"grace"})
	if f.memberCalls != calls {
		t.Fatal("member list not cached")
	}
	for _, tok := range f.tokens {
		if tok != "Bearer service-token" {
			t.Fatalf("calls carried %q, want the service token", tok)
		}
	}
	if len(f.tokens) == 0 {
		t.Fatal("no call carried the service token")
	}
}
