package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/genuinebnt/skein-go/services/notifications/internal/domain"
	"github.com/genuinebnt/skein-go/services/notifications/internal/email"
)

// EmailSenderConfig wires an EmailSender. Zero values take the defaults.
type EmailSenderConfig struct {
	Store     Store
	Directory Directory
	Mailer    Mailer
	Renderer  *email.Renderer
	Clock     Clock
	NewID     IDGenerator

	MaxPerEmail    int           // notifications per digest; 50
	RecipientBatch int           // recipients planned per pass; 100
	ClaimBatch     int           // sends claimed per pass; 20
	Concurrency    int           // sends in flight; 4
	Lease          time.Duration // how long a claimed send is ours; 2m
	MaxAttempts    int           // before dead-lettering; 8
	InitialBackoff time.Duration // 30s
	MaxBackoff     time.Duration // 1h
	PollInterval   time.Duration // 5s
	SendTimeout    time.Duration // per email; 30s
	// Jitter maps a backoff to the delay used; default uniform in
	// [d/2, d], so sends that failed together do not retry together.
	Jitter func(time.Duration) time.Duration

	Logger        *slog.Logger
	MeterProvider metric.MeterProvider
}

// EmailSender drains the email outbox. See the package documentation for
// the delivery guarantee.
type EmailSender struct {
	cfg  EmailSenderConfig
	wake chan struct{}

	sent     metric.Int64Counter
	duration metric.Float64Histogram
}

// EmailPassResult reports one RunOnce.
type EmailPassResult struct {
	Planned, Skipped, Claimed, Sent, Retried, Dead, Dropped int
}

// NewEmailSender checks the wiring and returns a sender.
func NewEmailSender(cfg EmailSenderConfig) (*EmailSender, error) {
	if cfg.Store == nil || cfg.Directory == nil || cfg.Mailer == nil || cfg.Renderer == nil || cfg.NewID == nil {
		return nil, errors.New("app: email sender needs a store, directory, mailer, renderer and ID generator")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	setDefault(&cfg.MaxPerEmail, 50)
	setDefault(&cfg.RecipientBatch, 100)
	setDefault(&cfg.ClaimBatch, 20)
	setDefault(&cfg.Concurrency, 4)
	setDefault(&cfg.Lease, 2*time.Minute)
	setDefault(&cfg.MaxAttempts, 8)
	setDefault(&cfg.InitialBackoff, 30*time.Second)
	setDefault(&cfg.MaxBackoff, time.Hour)
	setDefault(&cfg.PollInterval, 5*time.Second)
	setDefault(&cfg.SendTimeout, 30*time.Second)
	if cfg.Jitter == nil {
		cfg.Jitter = func(d time.Duration) time.Duration { return d/2 + rand.N(d/2+1) }
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.MeterProvider == nil {
		cfg.MeterProvider = otel.GetMeterProvider()
	}
	meter := cfg.MeterProvider.Meter("github.com/genuinebnt/skein-go/services/notifications/internal/app")
	sent, err1 := meter.Int64Counter("skein.notifications.emails", metric.WithUnit("{email}"),
		metric.WithDescription("Email send attempts by outcome (sent, retry, dead_lettered, dropped) and type (single, digest)."))
	duration, err2 := meter.Float64Histogram("skein.notifications.email.send.duration", metric.WithUnit("s"),
		metric.WithDescription("Time to hand one email to the SMTP server."),
		metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10))
	if err := errors.Join(err1, err2); err != nil {
		return nil, fmt.Errorf("app: metrics: %w", err)
	}
	return &EmailSender{cfg: cfg, wake: make(chan struct{}, 1), sent: sent, duration: duration}, nil
}

func setDefault[T comparable](v *T, def T) {
	var zero T
	if *v == zero {
		*v = def
	}
}

// Notify wakes Run early, after a commit that enqueued email. It never
// blocks.
func (s *EmailSender) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run sends until ctx ends. A send in flight when ctx ends still records
// its outcome, on a context without the cancellation, so an ordinary
// shutdown does not leave a sent email to be sent again.
func (s *EmailSender) Run(ctx context.Context) error {
	for {
		if _, err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			s.cfg.Logger.ErrorContext(ctx, "email pass failed", slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.wake:
			// Let the digest delay elapse; the poll catches the job when due.
		case <-time.After(s.cfg.PollInterval):
		}
	}
}

// RunOnce plans due jobs into sends, claims due sends and sends them.
func (s *EmailSender) RunOnce(ctx context.Context) (EmailPassResult, error) {
	var res EmailPassResult
	now := domain.Microseconds(s.cfg.Clock())
	if err := s.plan(ctx, now, &res); err != nil {
		return res, err
	}
	var sends []EmailSend
	err := s.cfg.Store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		sends, err = tx.ClaimEmailSends(ctx, now, s.cfg.Lease, s.cfg.ClaimBatch)
		return err
	})
	if err != nil {
		return res, fmt.Errorf("app: claim email sends: %w", err)
	}
	res.Claimed = len(sends)

	var mu sync.Mutex
	var wg sync.WaitGroup
	limit := make(chan struct{}, s.cfg.Concurrency)
	var errs []error
	for _, send := range sends {
		if ctx.Err() != nil {
			break // claimed but unsent: the lease expires and another pass takes them
		}
		limit <- struct{}{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-limit }()
			outcome := s.attempt(ctx, send)
			err := s.finish(ctx, send, outcome)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			switch {
			case outcome.State == EmailSent && len(send.Notifications) == 0:
				res.Dropped++
			case outcome.State == EmailSent:
				res.Sent++
			case outcome.State == EmailPending:
				res.Retried++
			case outcome.State == EmailDead:
				res.Dead++
			}
		}()
	}
	wg.Wait()
	return res, errors.Join(errs...)
}

func (s *EmailSender) plan(ctx context.Context, now time.Time, res *EmailPassResult) error {
	err := s.cfg.Store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		pending, err := tx.PendingEmails(ctx, now, s.cfg.RecipientBatch)
		if err != nil {
			return err
		}
		plan := domain.PlanEmails(pending, now, s.cfg.MaxPerEmail)
		if len(plan.Skipped) > 0 {
			if err := tx.SkipEmailJobs(ctx, plan.Skipped); err != nil {
				return err
			}
		}
		for _, b := range plan.Batches {
			ids := make([]string, len(b.Jobs))
			for i, j := range b.Jobs {
				ids[i] = j.JobID
			}
			send := EmailSend{ID: s.cfg.NewID(), RecipientID: b.RecipientID, State: EmailPending, NextAttemptAt: now, CreatedAt: now}
			if err := tx.CreateEmailSend(ctx, send, ids); err != nil {
				return err
			}
		}
		res.Planned, res.Skipped = len(plan.Batches), len(plan.Skipped)
		return nil
	})
	if err != nil {
		return fmt.Errorf("app: plan email sends: %w", err)
	}
	return nil
}

// attempt renders and sends one claimed send and decides its outcome.
func (s *EmailSender) attempt(ctx context.Context, send EmailSend) EmailOutcome {
	if len(send.Notifications) == 0 {
		// Every notification was purged since the job was written.
		return EmailOutcome{State: EmailSent, At: s.cfg.Clock(), Error: "nothing left to send"}
	}
	prof, err := s.cfg.Directory.Profile(ctx, send.RecipientID)
	switch {
	case errors.Is(err, ErrUnknownUser):
		return s.failed(send, fmt.Errorf("%w: %w", ErrPermanent, err))
	case err != nil:
		return s.failed(send, err)
	case prof.Email == "":
		return s.failed(send, fmt.Errorf("%w: recipient has no email address", ErrPermanent))
	}
	msg, err := s.cfg.Renderer.Render(send.ID, email.Recipient{Name: prof.DisplayName, Address: prof.Email}, send.Notifications)
	if err != nil {
		return s.failed(send, fmt.Errorf("%w: %w", ErrPermanent, err))
	}
	sendCtx, cancel := context.WithTimeout(ctx, s.cfg.SendTimeout)
	defer cancel()
	start := time.Now()
	err = s.cfg.Mailer.Send(sendCtx, msg)
	s.duration.Record(ctx, time.Since(start).Seconds())
	if err != nil {
		return s.failed(send, err)
	}
	return EmailOutcome{State: EmailSent, At: s.cfg.Clock()}
}

func (s *EmailSender) failed(send EmailSend, err error) EmailOutcome {
	now := s.cfg.Clock()
	if errors.Is(err, ErrPermanent) || send.Attempts >= s.cfg.MaxAttempts {
		return EmailOutcome{State: EmailDead, At: now, Error: err.Error()}
	}
	delay := s.cfg.Jitter(domain.RetryDelay(send.Attempts, s.cfg.InitialBackoff, s.cfg.MaxBackoff))
	return EmailOutcome{State: EmailPending, At: now, RetryAt: domain.Microseconds(now.Add(delay)), Error: err.Error()}
}

// finish records the outcome on a context that survives shutdown: the
// email may already be out, and forgetting that is what would send it
// twice.
func (s *EmailSender) finish(ctx context.Context, send EmailSend, outcome EmailOutcome) error {
	outcome.At = domain.Microseconds(outcome.At)
	ctx = context.WithoutCancel(ctx)
	err := s.cfg.Store.WithTx(ctx, func(ctx context.Context, tx Tx) error {
		return tx.FinishEmailSend(ctx, send.ID, send.Attempts, outcome)
	})
	kind := "single"
	if len(send.Notifications) > 1 {
		kind = "digest"
	}
	label := map[EmailSendState]string{EmailSent: "sent", EmailPending: "retry", EmailDead: "dead_lettered"}[outcome.State]
	if outcome.State == EmailSent && len(send.Notifications) == 0 {
		label = "dropped"
	}
	s.sent.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", label), attribute.String("type", kind)))
	log := s.cfg.Logger.With(slog.String("send_id", send.ID), slog.String("user_id", send.RecipientID), slog.Int("attempt", send.Attempts))
	switch outcome.State {
	case EmailDead:
		log.ErrorContext(ctx, "email dead-lettered", slog.String("error", outcome.Error))
	case EmailPending:
		log.WarnContext(ctx, "email send failed; will retry", slog.Time("retry_at", outcome.RetryAt), slog.String("error", outcome.Error))
	}
	if err != nil {
		return fmt.Errorf("app: record email outcome: %w", err)
	}
	return nil
}
