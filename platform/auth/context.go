package auth

import "context"

type contextKey int

const (
	claimsKey contextKey = iota
	tokenKey
)

// WithClaims returns ctx carrying verified claims and the raw token they
// came from. Only the authentication middleware should call it: anything
// in the context under this key is trusted as authenticated.
func WithClaims(ctx context.Context, claims Claims, rawToken string) context.Context {
	ctx = context.WithValue(ctx, claimsKey, claims)
	return context.WithValue(ctx, tokenKey, rawToken)
}

// ClaimsFromContext returns the verified claims of the caller.
func ClaimsFromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey).(Claims)
	return c, ok
}

// SubjectFromContext returns the verified subject of the caller.
func SubjectFromContext(ctx context.Context) (Subject, bool) {
	c, ok := ClaimsFromContext(ctx)
	return c.Subject, ok
}

// TokenFromContext returns the caller's raw bearer token, so a service
// calling another service on the user's behalf can forward it (grpcx
// clients do this automatically).
func TokenFromContext(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(tokenKey).(string)
	return t, ok && t != ""
}
