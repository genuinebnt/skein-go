// Package identity implements app.Authorizer and app.Directory with
// identity's gRPC API (Authorize, GetUser, ListMembers), behind short-lived
// caches.
//
// # Caches
//
//	cache        key                    TTL   invalidated by
//	authz        user/action/resource   30s   AccessChanged for the user (tag "authz-user:<id>")
//	profile      user                   10m   nothing (names and addresses change rarely)
//	handles      workspace              60s   nothing (TTL only; see below)
//
// Authorization answers are what every consumed event asks, often for the
// same users and notes, so caching them saves most of the RPCs; the
// AccessChanged consumer drops a user's answers before acting, which is
// what keeps cached authorization correct (docs/observability.md calls this
// the "event" invalidation reason). All caches use platform/redisx, shared
// by the replicas through Redis, and fail open (a Redis outage means more
// RPCs, not errors).
//
// # Handles
//
// identity has no handle field yet. Until it does (a proposed contract
// change, see the service README), a member's handle is the local part of
// their email address, lowercased: the rule the seed data follows for
// every user. The member list of a workspace is cached for 60 seconds, so
// someone who joined in the last minute may not be resolvable yet.
//
// # Credentials
//
// Calls made while handling an event carry no user token. When
// IDENTITY_SERVICE_TOKEN is set it is sent as the bearer token of every
// call; otherwise the caller's token is forwarded by grpcx as usual.
// Service-to-service identity is a platform follow-up.
//
// # Porting notes
//
// Rust: the tonic-generated client behind the same three-method trait;
// the caches on the same Redis keys.
package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
)

// Client is the part of the generated IdentityServiceClient this package
// uses; tests implement it in memory.
type Client interface {
	Authorize(ctx context.Context, in *skeinv1.AuthorizeRequest, opts ...grpc.CallOption) (*skeinv1.AuthorizeResponse, error)
	GetUser(ctx context.Context, in *skeinv1.GetUserRequest, opts ...grpc.CallOption) (*skeinv1.GetUserResponse, error)
	ListMembers(ctx context.Context, in *skeinv1.ListMembersRequest, opts ...grpc.CallOption) (*skeinv1.ListMembersResponse, error)
}

// Options configure New.
type Options struct {
	// ServiceToken, when set, authenticates calls made without a user.
	ServiceToken  config.Secret
	AuthzTTL      time.Duration // 30s
	ProfileTTL    time.Duration // 10m
	HandlesTTL    time.Duration // 60s
	MeterProvider metric.MeterProvider
}

// Identity is the adapter.
type Identity struct {
	client      Client
	token       config.Secret
	authz       *redisx.Cache[bool]
	profiles    *redisx.Cache[app.Profile]
	handles     *redisx.Cache[map[string]string]
	invalidator *redisx.Invalidator
}

// New returns the adapter with its caches on store.
func New(client Client, store redisx.Store, opts Options) (*Identity, error) {
	ttl := func(d, def time.Duration) time.Duration {
		if d == 0 {
			return def
		}
		return d
	}
	authz, err1 := redisx.New(store, redisx.Options[bool]{Name: "authz", TTL: ttl(opts.AuthzTTL, 30*time.Second), MeterProvider: opts.MeterProvider})
	profiles, err2 := redisx.New(store, redisx.Options[app.Profile]{Name: "profile", TTL: ttl(opts.ProfileTTL, 10*time.Minute), MeterProvider: opts.MeterProvider})
	handles, err3 := redisx.New(store, redisx.Options[map[string]string]{Name: "handles", TTL: ttl(opts.HandlesTTL, time.Minute), MeterProvider: opts.MeterProvider})
	inv, err4 := redisx.NewInvalidator(store, opts.MeterProvider)
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		return nil, fmt.Errorf("identity: caches: %w", err)
	}
	return &Identity{client: client, token: opts.ServiceToken, authz: authz, profiles: profiles, handles: handles, invalidator: inv}, nil
}

func (i *Identity) outgoing(ctx context.Context) context.Context {
	if i.token.Reveal() == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+i.token.Reveal())
}

func authzTag(userID string) string { return "authz-user:" + userID }

// Authorize implements app.Authorizer. at is ignored: identity answers for
// the present.
func (i *Identity) Authorize(ctx context.Context, userID string, checks []app.Check, _ time.Time) ([]bool, error) {
	out := make([]bool, len(checks))
	var missing []int
	for idx, c := range checks {
		v, found, err := i.authz.Get(ctx, authzKey(userID, c))
		if err == nil && found {
			out[idx] = v
			continue
		}
		missing = append(missing, idx)
	}
	if len(missing) == 0 {
		return out, nil
	}
	req := &skeinv1.AuthorizeRequest{Subject: &skeinv1.Subject{UserId: userID}}
	for _, idx := range missing {
		req.Checks = append(req.Checks, &skeinv1.Check{Action: skeinv1.Action(checks[idx].Action), ResourceId: checks[idx].ResourceID})
	}
	resp, err := i.client.Authorize(i.outgoing(ctx), req)
	if err != nil {
		return nil, fmt.Errorf("identity: authorize: %w", err)
	}
	if len(resp.GetAllowed()) != len(missing) {
		return nil, fmt.Errorf("identity: authorize returned %d answers for %d checks", len(resp.GetAllowed()), len(missing))
	}
	for k, idx := range missing {
		out[idx] = resp.GetAllowed()[k]
		// A failed cache write only costs a later RPC.
		_ = i.authz.Set(ctx, authzKey(userID, checks[idx]), out[idx], authzTag(userID))
	}
	return out, nil
}

func authzKey(userID string, c app.Check) string {
	return fmt.Sprintf("%s/%d/%s", userID, c.Action, c.ResourceID)
}

// InvalidateUser drops the user's cached authorization answers; the
// identity.events consumer calls it on AccessChanged.
func (i *Identity) InvalidateUser(ctx context.Context, userID string) error {
	_, err := i.invalidator.InvalidateTags(ctx, authzTag(userID))
	return err
}

// Profile implements app.Directory.
func (i *Identity) Profile(ctx context.Context, userID string) (app.Profile, error) {
	return i.profiles.GetOrLoad(ctx, userID, func(ctx context.Context) (app.Profile, []string, error) {
		resp, err := i.client.GetUser(i.outgoing(ctx), &skeinv1.GetUserRequest{UserId: userID})
		if status.Code(err) == codes.NotFound {
			return app.Profile{}, nil, fmt.Errorf("%w: %s", app.ErrUnknownUser, userID)
		}
		if err != nil {
			return app.Profile{}, nil, fmt.Errorf("identity: get user: %w", err)
		}
		u := resp.GetUser()
		return app.Profile{UserID: userID, DisplayName: u.GetDisplayName(), Email: u.GetEmail()}, nil, nil
	})
}

// HandleOf derives a member's handle from their email address (package
// documentation).
func HandleOf(email string) string {
	local, _, found := strings.Cut(email, "@")
	if !found {
		return ""
	}
	return strings.ToLower(local)
}

// maxMemberPages bounds the member listing of one workspace.
const maxMemberPages = 100

// ResolveHandles implements app.Directory.
func (i *Identity) ResolveHandles(ctx context.Context, workspaceID string, handles []string) (map[string]string, error) {
	members, err := i.handles.GetOrLoad(ctx, workspaceID, func(ctx context.Context) (map[string]string, []string, error) {
		byHandle := map[string]string{}
		req := &skeinv1.ListMembersRequest{WorkspaceId: workspaceID, Page: &skeinv1.PageRequest{PageSize: 500}}
		for range maxMemberPages {
			resp, err := i.client.ListMembers(i.outgoing(ctx), req)
			if err != nil {
				return nil, nil, fmt.Errorf("identity: list members: %w", err)
			}
			for _, m := range resp.GetMembers() {
				if h := HandleOf(m.GetUser().GetEmail()); h != "" {
					byHandle[h] = m.GetUser().GetId()
				}
			}
			next := resp.GetPage().GetNextPageToken()
			if next == "" {
				break
			}
			req.Page.PageToken = next
		}
		return byHandle, nil, nil
	})
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, h := range handles {
		if id, ok := members[h]; ok {
			out[h] = id
		}
	}
	return out, nil
}

// CheckActions lists the actions this service asks identity about, for
// tests that the enum values line up with the proto.
var CheckActions = []app.Action{app.ActionNoteRead, app.ActionNoteComment, app.ActionNoteEdit, app.ActionNoteCreate}
