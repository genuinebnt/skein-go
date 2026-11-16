// Package oauth implements app.OAuthProvider for Google and GitHub: the
// authorization-code exchange with PKCE (RFC 7636), then a profile read.
//
// The browser starts the flow with an S256 code challenge and returns the
// code; identity sends the code, the verifier and the redirect URI to the
// provider's token endpoint, which checks the verifier against the
// challenge. A stolen code is useless without the verifier, which never
// left the browser.
//
//   - Google: POST the token endpoint, then GET the OpenID Connect userinfo
//     endpoint with the access token (sub, email, email_verified, name).
//     Reading userinfo over TLS from Google avoids verifying the ID token's
//     signature here, at the cost of one request.
//   - GitHub: POST the token endpoint (JSON requested; errors arrive as 200
//     with an "error" field), then GET /user for the stable numeric ID and
//     /user/emails for the primary address and whether it is verified.
//
// Provider refusals (invalid_grant, bad verifier, 4xx) are
// app.ErrOAuthRejected (UNAUTHENTICATED); network failures and 5xx are
// app.ErrProviderUnavailable (UNAVAILABLE). Response bodies are capped and
// never logged: they hold tokens.
package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/genuinebnt/skein-go/platform/config"

	"github.com/genuinebnt/skein-go/services/identity/internal/app"
)

const maxBody = 1 << 20

// Endpoints are a provider's URLs; tests point them at httptest servers.
type Endpoints struct {
	Token   string
	Profile string
	Emails  string // GitHub only
}

// GoogleEndpoints and GitHubEndpoints are the production URLs.
var (
	GoogleEndpoints = Endpoints{Token: "https://oauth2.googleapis.com/token", Profile: "https://openidconnect.googleapis.com/v1/userinfo"}
	GitHubEndpoints = Endpoints{Token: "https://github.com/login/oauth/access_token", Profile: "https://api.github.com/user", Emails: "https://api.github.com/user/emails"}
)

// Client is an OAuth client registration.
type Client struct {
	ID        string
	Secret    config.Secret
	Endpoints Endpoints
	HTTP      *http.Client // a client with a 10-second timeout when nil
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// exchange posts the code to the token endpoint and returns the access token.
func (c Client) exchange(ctx context.Context, code, verifier, redirectURI string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
		"client_id":     {c.ID},
		"client_secret": {c.Secret.Reveal()},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoints.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := c.do(req, &body); err != nil {
		return "", err
	}
	if body.Error != "" || body.AccessToken == "" {
		// The provider's error code is safe to report; descriptions are not
		// needed and may echo input.
		return "", fmt.Errorf("token endpoint refused the code (%s): %w", body.Error, app.ErrOAuthRejected)
	}
	return body.AccessToken, nil
}

// get reads a JSON resource with the access token.
func (c Client) get(ctx context.Context, endpoint, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	return c.do(req, out)
}

func (c Client) do(req *http.Request, out any) error {
	resp, err := c.http().Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w: %v", req.Method, req.URL.Host, app.ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("%s %s: %w: %v", req.Method, req.URL.Host, app.ErrProviderUnavailable, err)
	}
	switch {
	case resp.StatusCode >= 500:
		return fmt.Errorf("%s %s: status %d: %w", req.Method, req.URL.Host, resp.StatusCode, app.ErrProviderUnavailable)
	case resp.StatusCode >= 400:
		return fmt.Errorf("%s %s: status %d: %w", req.Method, req.URL.Host, resp.StatusCode, app.ErrOAuthRejected)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: undecodable response: %w", req.Method, req.URL.Host, app.ErrProviderUnavailable)
	}
	return nil
}

// Google is the Google provider.
type Google struct{ Client Client }

// Exchange implements app.OAuthProvider.
func (g Google) Exchange(ctx context.Context, code, verifier, redirectURI string) (app.ExternalIdentity, error) {
	token, err := g.Client.exchange(ctx, code, verifier, redirectURI)
	if err != nil {
		return app.ExternalIdentity{}, fmt.Errorf("google: %w", err)
	}
	var info struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := g.Client.get(ctx, g.Client.Endpoints.Profile, token, &info); err != nil {
		return app.ExternalIdentity{}, fmt.Errorf("google userinfo: %w", err)
	}
	return app.ExternalIdentity{Subject: info.Sub, Email: info.Email, EmailVerified: info.EmailVerified, DisplayName: info.Name}, nil
}

// GitHub is the GitHub provider.
type GitHub struct{ Client Client }

// Exchange implements app.OAuthProvider.
func (g GitHub) Exchange(ctx context.Context, code, verifier, redirectURI string) (app.ExternalIdentity, error) {
	token, err := g.Client.exchange(ctx, code, verifier, redirectURI)
	if err != nil {
		return app.ExternalIdentity{}, fmt.Errorf("github: %w", err)
	}
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := g.Client.get(ctx, g.Client.Endpoints.Profile, token, &user); err != nil {
		return app.ExternalIdentity{}, fmt.Errorf("github user: %w", err)
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := g.Client.get(ctx, g.Client.Endpoints.Emails, token, &emails); err != nil {
		return app.ExternalIdentity{}, fmt.Errorf("github emails: %w", err)
	}
	id := app.ExternalIdentity{DisplayName: user.Name}
	if user.ID > 0 {
		id.Subject = strconv.FormatInt(user.ID, 10)
	}
	if id.DisplayName == "" {
		id.DisplayName = user.Login
	}
	for _, e := range emails {
		if e.Primary {
			id.Email, id.EmailVerified = e.Email, e.Verified
		}
	}
	return id, nil
}
