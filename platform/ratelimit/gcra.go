package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// gcraScript is the whole decision, run atomically by Redis.
//
// KEYS[1]  the key holding the TAT, in microseconds since the Unix epoch
// ARGV[1]  emission interval T in microseconds
// ARGV[2]  burst
// ARGV[3]  cost n
//
// Returns {allowed, remaining, reset_us, retry_after_us}.
//
// TAT is written with string.format('%d') so the stored value is an exact
// integer string. How Redis turns a Lua number argument into a string has
// changed between versions (%.17g, then shortest round-trip), and an
// exponent form such as "1.76e+15" would corrupt the next GET. miniredis
// does not reproduce that, so this is defensive. The key expires when the bucket is full again, so
// idle keys cost nothing.
const gcraScript = `
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local interval = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local cost = tonumber(ARGV[3])
local tolerance = interval * burst

local tat = tonumber(redis.call('GET', KEYS[1]))
if tat == nil or tat < now then
  tat = now
end
local new_tat = tat + interval * cost
local wait = new_tat - now

if wait > tolerance then
  local remaining = math.floor((tolerance - (tat - now)) / interval)
  return {0, remaining, tat - now, wait - tolerance}
end

redis.call('SET', KEYS[1], string.format('%d', new_tat), 'PX', math.ceil(wait / 1000))
return {1, math.floor((tolerance - wait) / interval), wait, 0}
`

// RedisGCRA is a Limiter shared by every process using the same Redis.
type RedisGCRA struct {
	client redis.Scripter
	prefix string
	script *redis.Script
}

// NewRedisGCRA returns a limiter storing state under keys "<prefix>:<key>".
// Use one prefix per budget ("rl:sync", "rl:search") so budgets never share
// state.
func NewRedisGCRA(client redis.Scripter, prefix string) *RedisGCRA {
	return &RedisGCRA{client: client, prefix: prefix, script: redis.NewScript(gcraScript)}
}

// Allow implements Limiter.
func (g *RedisGCRA) Allow(ctx context.Context, key string, limit Limit, n int64) (Decision, error) {
	if err := limit.validateCost(n); err != nil {
		return Decision{}, err
	}
	interval := limit.Period.Microseconds() / limit.Rate
	res, err := g.script.Run(ctx, g.client, []string{g.prefix + ":" + key}, interval, limit.Burst, n).Int64Slice()
	if err != nil {
		return Decision{}, fmt.Errorf("ratelimit: redis: %w", err)
	}
	if len(res) != 4 {
		return Decision{}, fmt.Errorf("ratelimit: redis: script returned %d values", len(res))
	}
	return Decision{
		Allowed:    res[0] == 1,
		Limit:      limit.Burst,
		Remaining:  res[1],
		Reset:      time.Duration(res[2]) * time.Microsecond,
		RetryAfter: time.Duration(res[3]) * time.Microsecond,
	}, nil
}
