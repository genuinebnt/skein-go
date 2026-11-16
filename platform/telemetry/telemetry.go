package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log"
	lognoop "go.opentelemetry.io/otel/log/noop"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// ErrUnsupportedExporter means an OTEL_*_EXPORTER variable names an
// exporter other than "otlp" or "none".
var ErrUnsupportedExporter = errors.New("telemetry: unsupported exporter")

// Config identifies the service on every span, metric and log record.
type Config struct {
	Service string
	Version string
	Env     string

	// Lookup reads environment variables; os.LookupEnv when nil. Tests
	// inject a map. The OTLP exporters still read the process environment
	// for their own settings.
	Lookup func(string) (string, bool)

	// Exporters replace the OTLP exporters, for tests. A nil field means
	// "use OTLP if that signal is enabled".
	SpanExporter sdktrace.SpanExporter
	MetricReader sdkmetric.Reader
	LogExporter  sdklog.Exporter
}

// Telemetry holds the configured providers.
type Telemetry struct {
	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	LoggerProvider log.LoggerProvider
	// Enabled is false when OTEL_SDK_DISABLED=true.
	Enabled bool

	logsOn    bool
	service   string
	shutdowns []func(context.Context) error
}

// Setup builds the providers, installs them and the W3C propagator as the
// OpenTelemetry globals, and returns them. Call Shutdown before exit to
// flush buffered telemetry. Exporters connect lazily, so Setup succeeds even
// when the collector is down; spans are dropped until it comes up.
func Setup(ctx context.Context, cfg Config) (*Telemetry, error) {
	lookup := cfg.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	if disabled(lookup) {
		t := &Telemetry{
			TracerProvider: tracenoop.NewTracerProvider(),
			MeterProvider:  metricnoop.NewMeterProvider(),
			LoggerProvider: lognoop.NewLoggerProvider(),
			service:        cfg.Service,
		}
		t.install()
		return t, nil
	}

	res, err := newResource(ctx, cfg)
	if err != nil {
		return nil, err
	}
	t := &Telemetry{Enabled: true, service: cfg.Service}
	if err := t.setupTraces(ctx, cfg, lookup, res); err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	if err := t.setupMetrics(ctx, cfg, lookup, res); err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	if err := t.setupLogs(ctx, cfg, lookup, res); err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	t.install()
	return t, nil
}

func (t *Telemetry) install() {
	otel.SetTracerProvider(t.TracerProvider)
	otel.SetMeterProvider(t.MeterProvider)
}

// Shutdown flushes and stops every provider, in reverse order of creation,
// and returns all errors joined. It is safe to call on a disabled setup.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for i := len(t.shutdowns) - 1; i >= 0; i-- {
		errs = append(errs, t.shutdowns[i](ctx))
	}
	t.shutdowns = nil
	return errors.Join(errs...)
}

// LogHandler returns an slog.Handler that exports records through the
// OpenTelemetry logs pipeline, or nil when logs export is off.
func (t *Telemetry) LogHandler() slog.Handler {
	if !t.logsOn {
		return nil
	}
	return otelslog.NewHandler(t.service, otelslog.WithLoggerProvider(t.LoggerProvider))
}

func disabled(lookup func(string) (string, bool)) bool {
	v, _ := lookup("OTEL_SDK_DISABLED")
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}

// exporterEnabled reads OTEL_<SIGNAL>_EXPORTER: unset or "otlp" means on,
// "none" means off.
func exporterEnabled(lookup func(string) (string, bool), signal string) (bool, error) {
	key := "OTEL_" + signal + "_EXPORTER"
	v, _ := lookup(key)
	switch strings.TrimSpace(strings.ToLower(v)) {
	case "", "otlp":
		return true, nil
	case "none":
		return false, nil
	default:
		return false, fmt.Errorf("%w: %s=%q (want otlp or none)", ErrUnsupportedExporter, key, v)
	}
}

func newResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	// Attribute keys are spelled out rather than taken from a semconv
	// package, so the resource does not pin a semconv version that may
	// differ from the one the SDK's default resource uses.
	base := resource.NewSchemaless(
		attribute.String("service.name", cfg.Service),
		attribute.String("service.version", cfg.Version),
		attribute.String("deployment.environment.name", cfg.Env),
	)
	detected, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithProcessRuntimeName(),
		resource.WithProcessRuntimeVersion(),
		resource.WithFromEnv(),
	)
	if err != nil && !errors.Is(err, resource.ErrPartialResource) {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}
	merged, err := resource.Merge(base, detected)
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}
	return merged, nil
}

func (t *Telemetry) setupTraces(ctx context.Context, cfg Config, lookup func(string) (string, bool), res *resource.Resource) error {
	on, err := exporterEnabled(lookup, "TRACES")
	if err != nil {
		return err
	}
	exporter := cfg.SpanExporter
	if exporter == nil && on {
		if exporter, err = otlptracegrpc.New(ctx); err != nil {
			return fmt.Errorf("telemetry: trace exporter: %w", err)
		}
	}
	opts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	if exporter != nil {
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	t.TracerProvider = tp
	t.shutdowns = append(t.shutdowns, tp.Shutdown)
	return nil
}

func (t *Telemetry) setupMetrics(ctx context.Context, cfg Config, lookup func(string) (string, bool), res *resource.Resource) error {
	on, err := exporterEnabled(lookup, "METRICS")
	if err != nil {
		return err
	}
	reader := cfg.MetricReader
	if reader == nil && on {
		exporter, err := otlpmetricgrpc.New(ctx)
		if err != nil {
			return fmt.Errorf("telemetry: metric exporter: %w", err)
		}
		reader = sdkmetric.NewPeriodicReader(exporter)
	}
	opts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	if reader != nil {
		opts = append(opts, sdkmetric.WithReader(reader))
	}
	mp := sdkmetric.NewMeterProvider(opts...)
	t.MeterProvider = mp
	t.shutdowns = append(t.shutdowns, mp.Shutdown)
	return nil
}

func (t *Telemetry) setupLogs(ctx context.Context, cfg Config, lookup func(string) (string, bool), res *resource.Resource) error {
	on, err := exporterEnabled(lookup, "LOGS")
	if err != nil {
		return err
	}
	exporter := cfg.LogExporter
	if exporter == nil && on {
		if exporter, err = otlploggrpc.New(ctx); err != nil {
			return fmt.Errorf("telemetry: log exporter: %w", err)
		}
	}
	if exporter == nil {
		t.LoggerProvider = lognoop.NewLoggerProvider()
		return nil
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)))
	t.LoggerProvider = lp
	t.logsOn = true
	t.shutdowns = append(t.shutdowns, lp.Shutdown)
	return nil
}
