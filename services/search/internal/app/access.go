package app

import (
	"context"
	"time"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/services/search/internal/domain"
)

// Access answers "which notes can this subject read in this workspace",
// asking identity and caching the answer.
//
// The answer is cached in Redis for ReadableTTL under the user and
// workspace, tagged with both (UserTag, WorkspaceTag), so an AccessChanged
// event drops it on every replica at once. Every search and graph call
// needs it, and identity computes it from memberships, overrides and share
// links, so caching it removes one RPC from nearly every request. An agent
// run reads exactly what its user reads (ADR-0009), so runs share the
// user's entry. If identity is unreachable, requests fail: permissions are
// never guessed.
type Access struct {
	directory AccessDirectory
	cache     *redisx.Cache[[]string]
}

// ReadableTTL bounds how long a revoked permission can linger if its
// AccessChanged event is lost or delayed.
const ReadableTTL = 30 * time.Second

// NewAccess returns an Access over directory, caching on store.
func NewAccess(directory AccessDirectory, store redisx.Store) (*Access, error) {
	cache, err := redisx.New[[]string](store, redisx.Options[[]string]{Name: "readable-notes", TTL: ReadableTTL})
	if err != nil {
		return nil, err
	}
	return &Access{directory: directory, cache: cache}, nil
}

// Readable returns the notes subject can read in workspace.
func (a *Access) Readable(ctx context.Context, subject auth.Subject, workspace string) (domain.Readable, error) {
	ids, err := a.cache.GetOrLoad(ctx, subject.UserID+"|"+workspace, func(ctx context.Context) ([]string, []string, error) {
		ids, err := a.directory.ListReadableNotes(ctx, auth.Subject{UserID: subject.UserID}, workspace)
		return ids, []string{UserTag(subject.UserID), WorkspaceTag(workspace)}, err
	})
	if err != nil {
		return nil, err
	}
	out := make(domain.Readable, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
