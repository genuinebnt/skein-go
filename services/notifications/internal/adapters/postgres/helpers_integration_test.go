//go:build integration

package postgres_test

import (
	"context"

	"github.com/genuinebnt/skein-go/services/notifications/internal/app"
	"github.com/genuinebnt/skein-go/services/notifications/internal/email"
)

// anyone is a directory where every user exists with an address.
type anyone struct{}

func (anyone) Profile(_ context.Context, userID string) (app.Profile, error) {
	return app.Profile{UserID: userID, DisplayName: "User", Email: "user@example.com"}, nil
}

func (anyone) ResolveHandles(context.Context, string, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func newRenderer() (*email.Renderer, error) { return email.NewRenderer("http://localhost:3000") }
