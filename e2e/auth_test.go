//go:build e2e

package e2e

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"

	skeinv1 "github.com/genuinebnt/skein-go/gen/go/skein/v1"
)

func TestSignUpSignInAndSignOut(t *testing.T) {
	t.Parallel()
	u := signUp(t, "ada")

	var in skeinv1.SignInResponse
	decode(t, "sign in", rawRequest(t, http.MethodPost, gatewayURL+"/v1/sessions", "",
		&skeinv1.SignInRequest{Email: u.email, Password: u.password}), &in)
	if in.GetUser().GetId() != u.id || in.GetSession().GetAccessToken() == "" {
		t.Fatalf("sign in returned %v", &in)
	}

	wrong := rawRequest(t, http.MethodPost, gatewayURL+"/v1/sessions", "",
		&skeinv1.SignInRequest{Email: u.email, Password: newPassword(t)})
	expectError(t, "sign in with a wrong password", wrong, http.StatusUnauthorized, int32(codes.Unauthenticated))

	var got skeinv1.GetUserResponse
	u.call(http.MethodGet, "/v1/users/"+u.id, nil, &got)
	if got.GetUser().GetEmail() != u.email {
		t.Fatalf("GetUser returned %v", &got)
	}
	anonymous := rawRequest(t, http.MethodGet, gatewayURL+"/v1/users/"+u.id, "", nil)
	expectError(t, "a request without a token", anonymous, http.StatusUnauthorized, int32(codes.Unauthenticated))

	decode(t, "sign out", rawRequest(t, http.MethodPost, gatewayURL+"/v1/sessions:signOut", "",
		&skeinv1.SignOutRequest{RefreshToken: in.GetSession().GetRefreshToken()}), nil)
	afterSignOut := rawRequest(t, http.MethodPost, gatewayURL+"/v1/sessions:refresh", "",
		&skeinv1.RefreshRequest{RefreshToken: in.GetSession().GetRefreshToken()})
	expectError(t, "refresh after sign out", afterSignOut, http.StatusUnauthorized, int32(codes.Unauthenticated))
}

// Refresh rotates the token; presenting a rotated token again means it was
// copied, so the whole session is revoked and the legitimate holder's newer
// token stops working too (services/identity/README.md, Sessions).
func TestRefreshRotatesAndReuseRevokesTheSession(t *testing.T) {
	t.Parallel()
	u := signUp(t, "grace")
	first := u.refresh

	refresh := func(token string) response {
		return rawRequest(t, http.MethodPost, gatewayURL+"/v1/sessions:refresh", "", &skeinv1.RefreshRequest{RefreshToken: token})
	}
	var second skeinv1.RefreshResponse
	decode(t, "first refresh", refresh(first), &second)
	if second.GetSession().GetRefreshToken() == first || second.GetSession().GetRefreshToken() == "" {
		t.Fatal("refresh did not rotate the token")
	}
	u.access = second.GetSession().GetAccessToken()
	u.call(http.MethodGet, "/v1/users/"+u.id, nil, nil)

	var third skeinv1.RefreshResponse
	decode(t, "second refresh", refresh(second.GetSession().GetRefreshToken()), &third)

	expectError(t, "reusing the first token", refresh(first), http.StatusUnauthorized, int32(codes.Unauthenticated))
	expectError(t, "the newest token after reuse", refresh(third.GetSession().GetRefreshToken()),
		http.StatusUnauthorized, int32(codes.Unauthenticated))
}
