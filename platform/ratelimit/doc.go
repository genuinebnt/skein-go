// Package ratelimit decides whether a request may proceed under a rate
// limit, and says how long to wait when it may not.
//
// # Model
//
// A Limit allows Rate events per Period on average, with bursts of up to
// Burst events at once from idle. Both implementations behind Limiter
// enforce exactly this model, so they agree on every decision for the same
// sequence of arrival times:
//
//   - GCRA (the generic cell rate algorithm) in Redis, for limits shared by
//     every gateway replica. State per key is one integer, the theoretical
//     arrival time (TAT): when the bucket would next be completely full
//     again, given the requests allowed so far. With emission interval
//     T = Period/Rate, a request of cost n at time now is allowed when
//
//     max(TAT, now) + n*T - now <= Burst*T
//
//     and then TAT becomes max(TAT, now) + n*T. The whole check-and-set is
//     one Lua script, so it is atomic, and it reads the time from Redis
//     (TIME), so replicas with skewed clocks share one timeline.
//
//   - TokenBucket in process, the second line of defence inside each
//     service. A bucket holds up to Burst tokens, refills at Rate per
//     Period and a request of cost n takes n tokens. Levels are integers
//     scaled by Period in nanoseconds, so the arithmetic is exact and the
//     decisions match GCRA's (the two are the same algorithm seen from
//     opposite ends: TAT - now is the missing tokens times T).
//
// # Decision
//
// Every call returns the numbers the edge needs for RateLimit-* and
// Retry-After headers: Limit (the burst), Remaining (requests of cost 1
// allowed right now after this one), Reset (time until the bucket is full)
// and RetryAfter (zero when allowed; otherwise the exact wait after which
// the same request would be allowed if nothing else arrives).
//
// # Invariants
//
// Property tests check these against a reference model in exact rational
// arithmetic, driven by a fake clock:
//
//   - Decisions equal the reference model's for any arrival sequence.
//   - No window of length w admits more than Burst + w*Rate/Period of cost.
//     In particular, never more than Burst at one instant.
//   - Under constant overload the admitted rate converges to Rate/Period.
//   - Waiting exactly RetryAfter makes a denied request allowed; waiting
//     one tick less does not.
//
// TokenBucket.CheckInvariants verifies 0 <= level <= capacity for every
// bucket it holds.
//
// # Precision
//
// GCRA counts microseconds (Redis TIME's resolution, and Lua numbers are
// doubles that stay exact for microsecond timestamps until the year 2255).
// T is Period/Rate rounded down to a whole microsecond, so a limit whose
// emission interval is not a whole number of microseconds admits up to one
// part in a million more than nominal. TokenBucket is exact.
//
// # Porting notes
//
// Rust: the same Lua script through `redis-rs` or `fred`'s script support;
// TokenBucket as a `Mutex<HashMap<String, Bucket>>` with `u128`
// intermediate products in place of the overflow checks here.
package ratelimit
