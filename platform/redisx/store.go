package redisx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrInvalidEntry means Set was called with a non-positive TTL or an empty
// key; entries without expiry are not allowed, so a forgotten invalidation
// can only ever serve stale data for one TTL.
var ErrInvalidEntry = errors.New("redisx: invalid cache entry")

// Store holds raw cache entries. Keys and tags here are already scoped to a
// cache by Cache; implementations add their own namespace.
type Store interface {
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration, tags ...string) error
	Delete(ctx context.Context, keys ...string) (removed int, err error)
	InvalidateTags(ctx context.Context, tags ...string) (removed int, err error)
}

func validateEntry(key string, ttl time.Duration) error {
	if key == "" || ttl <= 0 {
		return fmt.Errorf("%w: key %q ttl %v", ErrInvalidEntry, key, ttl)
	}
	return nil
}

// RedisStore is a Store in Redis under one namespace.
//
// Its scripts touch several keys, including keys read from a tag set
// rather than passed in, which Redis Cluster does not allow. Memorystore's standard tier is not clustered; a cluster
// deployment would need hash tags per tag group.
type RedisStore struct {
	client    redis.Cmdable
	namespace string
}

// NewRedisStore returns a store whose keys all start with namespace+":".
// Use the service name.
func NewRedisStore(client redis.Cmdable, namespace string) *RedisStore {
	return &RedisStore{client: client, namespace: namespace}
}

func (s *RedisStore) entryKey(key string) string { return s.namespace + ":c:" + key }
func (s *RedisStore) tagKey(tag string) string   { return s.namespace + ":t:" + tag }

// An entry is a hash: field "v" holds the value and one field per tag,
// named by the tag set's key, records that the entry carries that tag. Tag
// sets may keep stale members (an entry re-set without the tag, or one
// that expired); invalidation deletes a member only if the entry still has
// the tag's field, so stale members never remove the wrong entry. They
// leave with the tag set, whose expiry covers its longest-lived entry.

// setScript replaces the entry and adds it to each tag set, extending the
// set's expiry to cover the entry.
//
// KEYS[1] entry, KEYS[2..] tag sets; ARGV[1] value, ARGV[2] ttl ms.
var setScript = redis.NewScript(`
local ttl = tonumber(ARGV[2])
redis.call('DEL', KEYS[1])
redis.call('HSET', KEYS[1], 'v', ARGV[1])
for i = 2, #KEYS do
  redis.call('HSET', KEYS[1], KEYS[i], '1')
  redis.call('SADD', KEYS[i], KEYS[1])
  if redis.call('PTTL', KEYS[i]) < ttl then
    redis.call('PEXPIRE', KEYS[i], ttl)
  end
end
redis.call('PEXPIRE', KEYS[1], ttl)
return 1
`)

// invalidateScript deletes every entry that still carries one of the tags,
// then the tag sets.
//
// KEYS: tag sets. Returns the number of entries removed.
var invalidateScript = redis.NewScript(`
local removed = 0
for i = 1, #KEYS do
  local members = redis.call('SMEMBERS', KEYS[i])
  for _, entry in ipairs(members) do
    if redis.call('HEXISTS', entry, KEYS[i]) == 1 then
      removed = removed + redis.call('DEL', entry)
    end
  end
  redis.call('DEL', KEYS[i])
end
return removed
`)

// Get implements Store.
func (s *RedisStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	v, err := s.client.HGet(ctx, s.entryKey(key), "v").Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// Set implements Store.
func (s *RedisStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration, tags ...string) error {
	if err := validateEntry(key, ttl); err != nil {
		return err
	}
	keys := make([]string, 0, 1+len(tags))
	keys = append(keys, s.entryKey(key))
	for _, tag := range tags {
		keys = append(keys, s.tagKey(tag))
	}
	ms := max(ttl.Milliseconds(), 1)
	return setScript.Run(ctx, s.client, keys, value, ms).Err()
}

// Delete implements Store.
func (s *RedisStore) Delete(ctx context.Context, keys ...string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	full := make([]string, len(keys))
	for i, k := range keys {
		full[i] = s.entryKey(k)
	}
	n, err := s.client.Del(ctx, full...).Result()
	return int(n), err
}

// InvalidateTags implements Store.
func (s *RedisStore) InvalidateTags(ctx context.Context, tags ...string) (int, error) {
	if len(tags) == 0 {
		return 0, nil
	}
	keys := make([]string, len(tags))
	for i, tag := range tags {
		keys[i] = s.tagKey(tag)
	}
	n, err := invalidateScript.Run(ctx, s.client, keys).Int()
	return n, err
}

// MemoryStore is a Store in process with the same semantics as RedisStore.
type MemoryStore struct {
	now func() time.Time

	mu      sync.Mutex
	entries map[string]memoryEntry
	tags    map[string]map[string]struct{}
}

type memoryEntry struct {
	value   []byte
	expires time.Time
	tags    []string
}

// NewMemoryStore returns an empty store reading time from now (time.Now
// when nil).
func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{now: now, entries: map[string]memoryEntry{}, tags: map[string]map[string]struct{}{}}
}

// Get implements Store.
func (s *MemoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok {
		return nil, false, nil
	}
	if !s.now().Before(e.expires) {
		s.removeLocked(key)
		return nil, false, nil
	}
	return append([]byte(nil), e.value...), true, nil
}

// Set implements Store.
func (s *MemoryStore) Set(_ context.Context, key string, value []byte, ttl time.Duration, tags ...string) error {
	if err := validateEntry(key, ttl); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(key)
	s.entries[key] = memoryEntry{value: append([]byte(nil), value...), expires: s.now().Add(ttl), tags: append([]string(nil), tags...)}
	for _, tag := range tags {
		members, ok := s.tags[tag]
		if !ok {
			members = map[string]struct{}{}
			s.tags[tag] = members
		}
		members[key] = struct{}{}
	}
	return nil
}

// Delete implements Store.
func (s *MemoryStore) Delete(_ context.Context, keys ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for _, k := range keys {
		if s.liveLocked(k) {
			removed++
		}
		s.removeLocked(k)
	}
	return removed, nil
}

// InvalidateTags implements Store.
func (s *MemoryStore) InvalidateTags(_ context.Context, tags ...string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for _, tag := range tags {
		for key := range s.tags[tag] {
			if s.liveLocked(key) {
				removed++
			}
			s.removeLocked(key)
		}
		delete(s.tags, tag)
	}
	return removed, nil
}

func (s *MemoryStore) liveLocked(key string) bool {
	e, ok := s.entries[key]
	return ok && s.now().Before(e.expires)
}

func (s *MemoryStore) removeLocked(key string) {
	e, ok := s.entries[key]
	if !ok {
		return
	}
	delete(s.entries, key)
	for _, tag := range e.tags {
		if members, ok := s.tags[tag]; ok {
			delete(members, key)
			if len(members) == 0 {
				delete(s.tags, tag)
			}
		}
	}
}

// CheckInvariants verifies that the tag index and the entries agree: every
// tag member is an entry carrying that tag, every entry's tags list it, and
// no tag set is empty.
func (s *MemoryStore) CheckInvariants() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for tag, members := range s.tags {
		if len(members) == 0 {
			return fmt.Errorf("redisx: invariant: tag %q has no members", tag)
		}
		for key := range members {
			e, ok := s.entries[key]
			if !ok {
				return fmt.Errorf("redisx: invariant: tag %q lists missing entry %q", tag, key)
			}
			if !slices.Contains(e.tags, tag) {
				return fmt.Errorf("redisx: invariant: tag %q lists %q, which does not carry it", tag, key)
			}
		}
	}
	for key, e := range s.entries {
		for _, tag := range e.tags {
			if _, ok := s.tags[tag][key]; !ok {
				return fmt.Errorf("redisx: invariant: entry %q carries tag %q but is not indexed", key, tag)
			}
		}
	}
	return nil
}
