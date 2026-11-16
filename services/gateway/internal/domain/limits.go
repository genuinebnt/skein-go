package domain

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/genuinebnt/skein-go/platform/ratelimit"
)

// ErrInvalidLimit means a limit string does not parse or describes a limit
// ratelimit cannot enforce.
var ErrInvalidLimit = errors.New("domain: invalid rate limit")

// BudgetKey selects one limit: a budget counted for one kind of subject.
type BudgetKey struct {
	Budget  Budget
	Subject SubjectKind
}

// Limits holds one limit per budget and subject kind.
type Limits map[BudgetKey]ratelimit.Limit

// DefaultLimits are the budgets the gateway starts with. Per-IP limits are
// twice the per-user ones so a few people behind one NAT are not limited
// as one person, while a single host spraying accounts still is.
//
//	api     600/min per user, bursts of 100: page loads fan out to several calls
//	search  120/min per user, bursts of 30: search-as-you-type is debounced
//	sync    1200/min per user, bursts of 200: clients batch keystrokes
//	agent   20/min per user, bursts of 5: each frame starts a model run
func DefaultLimits() Limits {
	perUser := map[Budget]ratelimit.Limit{
		BudgetAPI:    ratelimit.PerMinute(600, 100),
		BudgetSearch: ratelimit.PerMinute(120, 30),
		BudgetSync:   ratelimit.PerMinute(1200, 200),
		BudgetAgent:  ratelimit.PerMinute(20, 5),
	}
	limits := Limits{}
	for budget, l := range perUser {
		limits[BudgetKey{budget, SubjectUser}] = l
		limits[BudgetKey{budget, SubjectIP}] = ratelimit.Limit{Rate: 2 * l.Rate, Period: l.Period, Burst: 2 * l.Burst}
	}
	return limits
}

// Validate reports whether every budget has a valid limit for both
// subject kinds.
func (l Limits) Validate() error {
	var errs []error
	for _, b := range Budgets {
		for _, s := range []SubjectKind{SubjectUser, SubjectIP} {
			limit, ok := l[BudgetKey{b, s}]
			if !ok {
				errs = append(errs, fmt.Errorf("%w: no %s limit per %s", ErrInvalidLimit, b, s))
				continue
			}
			if err := limit.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("%s per %s: %w", b, s, err))
			}
		}
	}
	return errors.Join(errs...)
}

// ParseLimit reads "rate/period:burst", for example "600/1m:100" (600 per
// minute, bursts of 100). The period is a Go duration.
func ParseLimit(s string) (ratelimit.Limit, error) {
	rateAndPeriod, burstText, ok := strings.Cut(s, ":")
	if !ok {
		return ratelimit.Limit{}, fmt.Errorf("%w: %q is not rate/period:burst", ErrInvalidLimit, s)
	}
	rateText, periodText, ok := strings.Cut(rateAndPeriod, "/")
	if !ok {
		return ratelimit.Limit{}, fmt.Errorf("%w: %q is not rate/period:burst", ErrInvalidLimit, s)
	}
	rate, err := strconv.ParseInt(rateText, 10, 64)
	if err != nil {
		return ratelimit.Limit{}, fmt.Errorf("%w: rate in %q: %v", ErrInvalidLimit, s, err)
	}
	period, err := time.ParseDuration(periodText)
	if err != nil {
		return ratelimit.Limit{}, fmt.Errorf("%w: period in %q: %v", ErrInvalidLimit, s, err)
	}
	burst, err := strconv.ParseInt(burstText, 10, 64)
	if err != nil {
		return ratelimit.Limit{}, fmt.Errorf("%w: burst in %q: %v", ErrInvalidLimit, s, err)
	}
	limit := ratelimit.Limit{Rate: rate, Period: period, Burst: burst}
	if err := limit.Validate(); err != nil {
		return ratelimit.Limit{}, errors.Join(ErrInvalidLimit, err)
	}
	return limit, nil
}

// LimiterKey is the key a budget is counted under in the limiter. User IDs
// and IP keys cannot collide because the subject kind is part of the key.
func LimiterKey(budget Budget, subject SubjectKind, id string) string {
	return string(budget) + ":" + string(subject) + ":" + id
}

// IPKey is the identity an IP budget is counted for: the address itself
// for IPv4, its /64 for IPv6 (see the package documentation).
func IPKey(addr netip.Addr) string {
	addr = addr.Unmap()
	if addr.Is6() {
		prefix, err := addr.Prefix(64)
		if err == nil {
			return prefix.String()
		}
	}
	return addr.String()
}
