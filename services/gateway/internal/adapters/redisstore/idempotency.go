package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/genuinebnt/skein-go/services/gateway/internal/app"
	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

// IdempotencyStore implements app.IdempotencyStore on Redis.
type IdempotencyStore struct {
	client    redis.Cmdable
	namespace string
}

// NewIdempotencyStore returns a store whose keys start with namespace.
func NewIdempotencyStore(client redis.Cmdable, namespace string) *IdempotencyStore {
	return &IdempotencyStore{client: client, namespace: namespace}
}

func (s *IdempotencyStore) key(k string) string { return s.namespace + ":idem:" + k }

// ownerGuard returns 1 when KEYS[1] holds a pending record owned by
// ARGV[1], after doing what ARGV[2] says: "set" stores ARGV[3] with a TTL
// of ARGV[4] milliseconds, "del" deletes the key. It returns 0, changing
// nothing, when another request owns the key or it is gone.
var ownerGuard = redis.NewScript(`
local raw = redis.call('GET', KEYS[1])
if not raw then return 0 end
local ok, rec = pcall(cjson.decode, raw)
if not ok or type(rec) ~= 'table' or rec.state ~= 'pending' or rec.owner ~= ARGV[1] then return 0 end
if ARGV[2] == 'set' then
  redis.call('SET', KEYS[1], ARGV[3], 'PX', ARGV[4])
else
  redis.call('DEL', KEYS[1])
end
return 1
`)

// Reserve implements app.IdempotencyStore. SET NX and the GET that follows
// a refusal are separate commands; if the record expires in between, the
// reservation is retried once, which is enough because expiry cannot
// happen twice within a few milliseconds.
func (s *IdempotencyStore) Reserve(ctx context.Context, key string, pending domain.IdempotencyRecord, ttl time.Duration) (domain.IdempotencyRecord, bool, error) {
	data, err := json.Marshal(pending)
	if err != nil {
		return domain.IdempotencyRecord{}, false, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		ok, err := s.client.SetNX(ctx, s.key(key), data, ttl).Result()
		if err != nil {
			return domain.IdempotencyRecord{}, false, fmt.Errorf("redisstore: reserve: %w", err)
		}
		if ok {
			return domain.IdempotencyRecord{}, true, nil
		}
		raw, err := s.client.Get(ctx, s.key(key)).Bytes()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return domain.IdempotencyRecord{}, false, fmt.Errorf("redisstore: read record: %w", err)
		}
		var existing domain.IdempotencyRecord
		if err := json.Unmarshal(raw, &existing); err != nil {
			return domain.IdempotencyRecord{}, false, fmt.Errorf("redisstore: decode record: %w", err)
		}
		return existing, false, nil
	}
	return domain.IdempotencyRecord{}, false, errors.New("redisstore: reserve: key kept expiring")
}

// Complete implements app.IdempotencyStore.
func (s *IdempotencyStore) Complete(ctx context.Context, key, owner string, complete domain.IdempotencyRecord, ttl time.Duration) error {
	data, err := json.Marshal(complete)
	if err != nil {
		return err
	}
	return s.guarded(ctx, key, owner, "set", string(data), ttl.Milliseconds())
}

// Release implements app.IdempotencyStore.
func (s *IdempotencyStore) Release(ctx context.Context, key, owner string) error {
	return s.guarded(ctx, key, owner, "del", "", 0)
}

func (s *IdempotencyStore) guarded(ctx context.Context, key, owner, op, value string, ttlMillis int64) error {
	n, err := ownerGuard.Run(ctx, s.client, []string{s.key(key)}, owner, op, value, ttlMillis).Int()
	if err != nil {
		return fmt.Errorf("redisstore: %s: %w", op, err)
	}
	if n == 0 {
		return app.ErrNotOwner
	}
	return nil
}
