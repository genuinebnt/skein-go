package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

// Options configure New.
type Options struct {
	// Service, Version and Env are attached to every record.
	Service string
	Version string
	Env     string
	// Level is the minimum level; Info when nil.
	Level slog.Leveler
	// Writer receives JSON lines; os.Stdout when nil (containers ship
	// stdout to the log backend).
	Writer io.Writer
	// Extra are additional sinks, for example telemetry's OpenTelemetry log
	// bridge. Each receives the same redacted, enriched records. Nil
	// entries are ignored so callers can pass an optional sink directly.
	Extra []slog.Handler
}

// New returns a logger writing JSON to opts.Writer and to every extra sink.
func New(opts Options) *slog.Logger { return slog.New(NewHandler(opts)) }

// NewHandler returns the handler behind New, for callers that compose
// handlers themselves.
func NewHandler(opts Options) slog.Handler {
	w := opts.Writer
	if w == nil {
		w = os.Stdout
	}
	level := opts.Level
	if level == nil {
		level = slog.LevelInfo
	}
	sinks := []slog.Handler{slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})}
	for _, h := range opts.Extra {
		if h != nil {
			sinks = append(sinks, h)
		}
	}
	var inner slog.Handler = sinks[0]
	if len(sinks) > 1 {
		inner = fanout(sinks)
	}
	inner = inner.WithAttrs([]slog.Attr{
		slog.String("service", opts.Service),
		slog.String("version", opts.Version),
		slog.String("env", opts.Env),
	})
	return &handler{inner: inner, level: level}
}

// handler enriches records from the context and redacts them before any
// sink sees them.
type handler struct {
	inner slog.Handler
	level slog.Leveler
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level.Level() && h.inner.Enabled(ctx, l)
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
	out.AddAttrs(contextAttrs(ctx)...)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handler{inner: h.inner.WithAttrs(redactAttrs(attrs)), level: h.level}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{inner: h.inner.WithGroup(name), level: h.level}
}

func contextAttrs(ctx context.Context) []slog.Attr {
	if ctx == nil {
		return nil
	}
	var attrs []slog.Attr
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		attrs = append(attrs,
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()))
	}
	if id := RequestID(ctx); id != "" {
		attrs = append(attrs, slog.String("request_id", id))
	}
	if s, ok := ctx.Value(subjectKey).(subject); ok {
		if s.userID != "" {
			attrs = append(attrs, slog.String("user_id", s.userID))
		}
		if s.agentRunID != "" {
			attrs = append(attrs, slog.String("agent_run_id", s.agentRunID))
		}
	}
	return attrs
}

// fanout sends each record to several handlers. Go 1.26 adds
// slog.NewMultiHandler; this is the same idea for 1.24.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			errs = append(errs, h.Handle(ctx, r.Clone()))
		}
	}
	return errors.Join(errs...)
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}

// Discard returns a logger that drops everything, for tests that do not
// assert on logs.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }
