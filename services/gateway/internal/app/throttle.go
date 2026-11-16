package app

import (
	"context"
	"log/slog"
	"net/netip"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/genuinebnt/skein-go/platform/ratelimit"

	"github.com/genuinebnt/skein-go/services/gateway/internal/domain"
)

const meterName = "github.com/genuinebnt/skein-go/services/gateway"

// Verdict is the outcome of spending one request's budget.
type Verdict struct {
	// Decision is the one to report in RateLimit-* headers: the denial if
	// a check denied, otherwise the check with the fewest requests left.
	Decision ratelimit.Decision
	// Key says which budget and subject Decision belongs to.
	Key domain.BudgetKey
	// Checked is false when every check failed open; there is then nothing
	// to report.
	Checked bool
}

// Limited reports whether the request must be refused.
func (v Verdict) Limited() bool { return v.Checked && !v.Decision.Allowed }

// Throttler spends rate-limit budgets (see the package documentation).
type Throttler struct {
	limiter   ratelimit.Limiter
	limits    domain.Limits
	log       *slog.Logger
	decisions metric.Int64Counter
}

// NewThrottler returns a throttler over limiter. limits must pass
// Validate. A nil meter provider records nothing.
func NewThrottler(limiter ratelimit.Limiter, limits domain.Limits, log *slog.Logger, mp metric.MeterProvider) (*Throttler, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if mp == nil {
		mp = noop.NewMeterProvider()
	}
	decisions, err := mp.Meter(meterName).Int64Counter("skein.ratelimit.decisions",
		metric.WithUnit("{request}"), metric.WithDescription("Rate-limit decisions by budget, subject kind and outcome"))
	if err != nil {
		return nil, err
	}
	return &Throttler{limiter: limiter, limits: limits, log: log, decisions: decisions}, nil
}

// Check spends one unit of budget for ip and, when userID is set, for the
// user. The IP is checked first and a denial stops there, so a request
// refused per IP does not also spend the user's budget.
func (t *Throttler) Check(ctx context.Context, budget domain.Budget, userID string, ip netip.Addr) Verdict {
	type check struct {
		subject domain.SubjectKind
		id      string
	}
	checks := []check{{domain.SubjectIP, domain.IPKey(ip)}}
	if userID != "" {
		checks = append(checks, check{domain.SubjectUser, userID})
	}
	var verdict Verdict
	for _, c := range checks {
		key := domain.BudgetKey{Budget: budget, Subject: c.subject}
		d, err := t.limiter.Allow(ctx, domain.LimiterKey(budget, c.subject, c.id), t.limits[key], 1)
		if err != nil {
			t.record(ctx, key, "allowed", "limiter_error")
			t.log.WarnContext(ctx, "rate limiter failed; allowing request",
				slog.String("budget", string(budget)), slog.String("subject", string(c.subject)), slog.String("error", err.Error()))
			continue
		}
		if !d.Allowed {
			t.record(ctx, key, "limited", "")
			attrs := []any{slog.String("budget", string(budget)), slog.String("subject", string(c.subject)),
				slog.Duration("retry_after", d.RetryAfter)}
			if c.subject == domain.SubjectIP {
				// The IP belongs in this log line and nowhere else: it is how
				// an operator finds who is being limited (observability.md).
				attrs = append(attrs, slog.String("client_ip", c.id))
			}
			t.log.WarnContext(ctx, "rate limited", attrs...)
			return Verdict{Decision: d, Key: key, Checked: true}
		}
		t.record(ctx, key, "allowed", "")
		if !verdict.Checked || d.Remaining < verdict.Decision.Remaining {
			verdict = Verdict{Decision: d, Key: key, Checked: true}
		}
	}
	return verdict
}

func (t *Throttler) record(ctx context.Context, key domain.BudgetKey, decision, errorType string) {
	attrs := []attribute.KeyValue{
		attribute.String("skein.ratelimit.layer", "gateway"),
		attribute.String("skein.ratelimit.budget", string(key.Budget)),
		attribute.String("skein.ratelimit.subject", string(key.Subject)),
		attribute.String("skein.ratelimit.decision", decision),
	}
	if errorType != "" {
		attrs = append(attrs, attribute.String("error.type", errorType))
	}
	t.decisions.Add(ctx, 1, metric.WithAttributes(attrs...))
}
