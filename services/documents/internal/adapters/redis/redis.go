// Package redis holds the documents service's Redis adapters:
//
//   - NoteCache: note metadata (title, workspace, trash state). Every RPC
//     reads it and it changes rarely; writes delete the entry, so a stale
//     read lasts at most until the next write on this note, bounded by TTL.
//   - AuthCache: Authorize decisions per (subject, action, resource), for a
//     short TTL, tagged by user so identity's AccessChanged event drops all
//     of a user's decisions at once on every instance (the cache is shared).
//   - Presence: cursors per note with a TTL, so a client that vanishes
//     without closing disappears, and a newcomer sees who is there even
//     after the note's actor was evicted.
//
// Caches fail open (platform/redisx): when Redis is down, calls go to
// identity or the store directly.
package redis

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/genuinebnt/skein-go/libs/crdt"
	"github.com/genuinebnt/skein-go/platform/redisx"
	"github.com/genuinebnt/skein-go/services/documents/internal/domain"
	"github.com/genuinebnt/skein-go/services/documents/internal/ports"
)

// NoteCache implements ports.NoteCache.
type NoteCache struct {
	cache *redisx.Cache[domain.Note]
}

// NewNoteCache caches note metadata on store for ttl.
func NewNoteCache(store redisx.Store, ttl time.Duration) (*NoteCache, error) {
	c, err := redisx.New(store, redisx.Options[domain.Note]{Name: "documents-note", TTL: ttl})
	if err != nil {
		return nil, err
	}
	return &NoteCache{cache: c}, nil
}

var _ ports.NoteCache = (*NoteCache)(nil)

// Get implements ports.NoteCache; a Redis failure is a miss.
func (c *NoteCache) Get(ctx context.Context, id domain.NoteID) (domain.Note, bool) {
	n, ok, err := c.cache.Get(ctx, string(id))
	return n, ok && err == nil
}

// Set implements ports.NoteCache.
func (c *NoteCache) Set(ctx context.Context, n domain.Note) {
	_ = c.cache.Set(ctx, string(n.ID), n, "note:"+string(n.ID))
}

// Delete implements ports.NoteCache.
func (c *NoteCache) Delete(ctx context.Context, id domain.NoteID) {
	_ = c.cache.Delete(ctx, string(id))
}

// AuthCache wraps an Authorizer with a decision cache. It implements both
// ports.Authorizer and ports.AuthCache.
type AuthCache struct {
	inner ports.Authorizer
	cache *redisx.Cache[bool]
	inv   *redisx.Invalidator
}

// NewAuthCache caches inner's decisions on store for ttl.
func NewAuthCache(inner ports.Authorizer, store redisx.Store, ttl time.Duration) (*AuthCache, error) {
	c, err := redisx.New(store, redisx.Options[bool]{Name: "documents-authorize", TTL: ttl})
	if err != nil {
		return nil, err
	}
	inv, err := redisx.NewInvalidator(store, nil)
	if err != nil {
		return nil, err
	}
	return &AuthCache{inner: inner, cache: c, inv: inv}, nil
}

var (
	_ ports.Authorizer = (*AuthCache)(nil)
	_ ports.AuthCache  = (*AuthCache)(nil)
)

func decisionKey(s domain.Subject, c ports.Check) string {
	return s.UserID + "|" + s.AgentRunID + "|" + strconv.Itoa(int(c.Action)) + "|" + c.Resource
}

// Authorize implements ports.Authorizer: cached decisions are reused and
// only the rest go to identity, in one call.
func (a *AuthCache) Authorize(ctx context.Context, s domain.Subject, checks []ports.Check) ([]bool, error) {
	out := make([]bool, len(checks))
	var missing []ports.Check
	var where []int
	for i, c := range checks {
		if v, ok, err := a.cache.Get(ctx, decisionKey(s, c)); err == nil && ok {
			out[i] = v
			continue
		}
		missing, where = append(missing, c), append(where, i)
	}
	if len(missing) == 0 {
		return out, nil
	}
	got, err := a.inner.Authorize(ctx, s, missing)
	if err != nil {
		return nil, err
	}
	if len(got) != len(missing) {
		return nil, fmt.Errorf("authorize answered %d of %d checks", len(got), len(missing))
	}
	for k, i := range where {
		out[i] = got[k]
		_ = a.cache.Set(ctx, decisionKey(s, missing[k]), got[k], "user:"+s.UserID)
	}
	return out, nil
}

// ReadableNotes implements ports.Authorizer, uncached: it lists notes, and a
// stale list would show or hide notes after a change.
func (a *AuthCache) ReadableNotes(ctx context.Context, s domain.Subject, ws domain.WorkspaceID) ([]domain.NoteID, error) {
	return a.inner.ReadableNotes(ctx, s, ws)
}

// InvalidateUser implements ports.AuthCache.
func (a *AuthCache) InvalidateUser(ctx context.Context, userID string) error {
	_, err := a.inv.InvalidateTags(ctx, "user:"+userID)
	return err
}

// Presence implements ports.PresenceStore with one hash per note: field =
// replica, value = the cursor and its expiry. Expired fields are removed
// when listed; the key itself expires when nobody updates it.
type Presence struct {
	client goredis.Cmdable
	now    func() time.Time
}

// NewPresence stores presence through client.
func NewPresence(client goredis.Cmdable, now func() time.Time) *Presence {
	return &Presence{client: client, now: now}
}

var _ ports.PresenceStore = (*Presence)(nil)

type presenceValue struct {
	UserID  string `json:"u"`
	Anchor  string `json:"a"`
	Head    string `json:"h"`
	Expires int64  `json:"e"` // unix milliseconds
}

func presenceKey(id domain.NoteID) string { return "documents:presence:" + string(id) }

// Put implements ports.PresenceStore.
func (p *Presence) Put(ctx context.Context, id domain.NoteID, pr ports.Presence, ttl time.Duration) error {
	v, err := json.Marshal(presenceValue{pr.UserID, pr.Anchor, pr.Head, p.now().Add(ttl).UnixMilli()})
	if err != nil {
		return err
	}
	key := presenceKey(id)
	pipe := p.client.TxPipeline()
	pipe.HSet(ctx, key, strconv.FormatUint(uint64(pr.Replica), 10), v)
	pipe.PExpire(ctx, key, ttl)
	_, err = pipe.Exec(ctx)
	return err
}

// List implements ports.PresenceStore, ordered by replica.
func (p *Presence) List(ctx context.Context, id domain.NoteID) ([]ports.Presence, error) {
	key := presenceKey(id)
	all, err := p.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	now := p.now().UnixMilli()
	var out []ports.Presence
	var expired []string
	for field, raw := range all {
		replica, err := strconv.ParseUint(field, 10, 64)
		var v presenceValue
		if err != nil || json.Unmarshal([]byte(raw), &v) != nil || v.Expires <= now {
			expired = append(expired, field)
			continue
		}
		out = append(out, ports.Presence{UserID: v.UserID, Replica: crdt.ReplicaID(replica), Anchor: v.Anchor, Head: v.Head})
	}
	if len(expired) > 0 {
		_ = p.client.HDel(ctx, key, expired...).Err()
	}
	slices.SortFunc(out, func(a, b ports.Presence) int { return cmp.Compare(a.Replica, b.Replica) })
	return out, nil
}

// Remove implements ports.PresenceStore.
func (p *Presence) Remove(ctx context.Context, id domain.NoteID, replica crdt.ReplicaID) error {
	return p.client.HDel(ctx, presenceKey(id), strconv.FormatUint(uint64(replica), 10)).Err()
}
