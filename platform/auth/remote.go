package auth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"time"
)

// FetchFunc returns the current JWKS document, for example by calling
// identity over gRPC or GET /.well-known/jwks.json.
type FetchFunc func(ctx context.Context) ([]byte, error)

// RemoteKeySetOptions configure NewRemoteKeySet. Zero values take the
// defaults.
type RemoteKeySetOptions struct {
	// RefreshInterval is how long a fetched set is trusted: C in the
	// rotation procedure. Default 5 minutes.
	RefreshInterval time.Duration
	// MinRefetch bounds refetches triggered by unknown key IDs, so a flood
	// of tokens with random kids cannot turn into a flood of fetches.
	// Default 30 seconds.
	MinRefetch time.Duration
	Now        func() time.Time
}

// RemoteKeySet is a KeySource that caches a fetched JWKS, refreshes it
// periodically and refetches early when it meets an unknown key ID. When a
// refresh fails it keeps serving the last good set: identity being briefly
// down must not log every user out.
type RemoteKeySet struct {
	fetch FetchFunc
	opts  RemoteKeySetOptions

	mu          sync.Mutex
	set         KeySet
	fetchedAt   time.Time
	lastAttempt time.Time
	loaded      bool
}

// NewRemoteKeySet returns a key set backed by fetch. Nothing is fetched
// until the first lookup.
func NewRemoteKeySet(fetch FetchFunc, opts RemoteKeySetOptions) *RemoteKeySet {
	if opts.RefreshInterval == 0 {
		opts.RefreshInterval = 5 * time.Minute
	}
	if opts.MinRefetch == 0 {
		opts.MinRefetch = 30 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &RemoteKeySet{fetch: fetch, opts: opts}
}

// PublicKey implements KeySource. The lock is held across the fetch on
// purpose: concurrent lookups during a refresh wait for it instead of each
// starting their own.
func (r *RemoteKeySet) PublicKey(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.opts.Now()

	var refreshErr error
	if !r.loaded || now.Sub(r.fetchedAt) >= r.opts.RefreshInterval {
		refreshErr = r.refreshLocked(ctx, now)
	}
	key, err := r.set.PublicKey(ctx, keyID)
	if err == nil {
		return key, nil
	}
	if refreshErr == nil && now.Sub(r.lastAttempt) >= r.opts.MinRefetch {
		refreshErr = r.refreshLocked(ctx, now)
		if key, err = r.set.PublicKey(ctx, keyID); err == nil {
			return key, nil
		}
	}
	if !r.loaded && refreshErr != nil {
		return nil, refreshErr
	}
	return nil, err
}

func (r *RemoteKeySet) refreshLocked(ctx context.Context, now time.Time) error {
	r.lastAttempt = now
	data, err := r.fetch(ctx)
	if err != nil {
		return fmt.Errorf("%w: fetch: %v", ErrUnknownKey, err)
	}
	set, err := ParseJWKS(data)
	if err != nil {
		return errors.Join(ErrUnknownKey, err)
	}
	r.set, r.fetchedAt, r.loaded = set, now, true
	return nil
}
