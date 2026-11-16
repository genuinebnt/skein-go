package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

type memoryLogExporter struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (e *memoryLogExporter) Export(_ context.Context, rs []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range rs {
		e.records = append(e.records, r.Clone())
	}
	return nil
}
func (*memoryLogExporter) Shutdown(context.Context) error   { return nil }
func (*memoryLogExporter) ForceFlush(context.Context) error { return nil }

func TestDisabledSDKInstallsNoopProviders(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	tel, err := Setup(context.Background(), Config{
		Service: "svc", Lookup: env(map[string]string{"OTEL_SDK_DISABLED": "true"}),
		SpanExporter: spans,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("t").Start(context.Background(), "op")
	span.End()
	if tel.Enabled || span.SpanContext().IsValid() || tel.LogHandler() != nil {
		t.Fatal("disabled SDK must produce no-op spans and no log handler")
	}
	if err := tel.Shutdown(context.Background()); err != nil || len(spans.GetSpans()) != 0 {
		t.Fatalf("shutdown %v, spans %d", err, len(spans.GetSpans()))
	}
}

func TestSpansCarryServiceResourceAndEnvOverrides(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "k8s.pod.name=identity-0,service.version=9.9.9")
	spans := tracetest.NewInMemoryExporter()
	tel, err := Setup(context.Background(), Config{
		Service: "identity", Version: "1.2.3", Env: "test",
		Lookup:       env(map[string]string{"OTEL_METRICS_EXPORTER": "none", "OTEL_LOGS_EXPORTER": "none"}),
		SpanExporter: spans,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, span := tel.TracerProvider.Tracer("t").Start(context.Background(), "op")
	span.End()
	// The in-memory exporter forgets its spans on Shutdown, so flush instead.
	if err := tel.TracerProvider.(interface{ ForceFlush(context.Context) error }).ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tel.Shutdown(context.Background()) }()
	got := spans.GetSpans()
	if len(got) != 1 {
		t.Fatalf("want 1 exported span, got %d", len(got))
	}
	attrs := map[string]string{}
	for _, kv := range got[0].Resource.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	for k, want := range map[string]string{
		"service.name": "identity", "deployment.environment.name": "test",
		// Environment values win over code, as the specification requires.
		"service.version": "9.9.9", "k8s.pod.name": "identity-0",
	} {
		if attrs[k] != want {
			t.Errorf("resource %s = %q, want %q", k, attrs[k], want)
		}
	}
}

func TestPropagatorIsW3CTraceContextAndBaggage(t *testing.T) {
	if _, err := Setup(context.Background(), Config{Lookup: env(map[string]string{"OTEL_SDK_DISABLED": "true"})}); err != nil {
		t.Fatal(err)
	}
	carrier := propagation.MapCarrier{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"baggage":     "tenant=w1",
	}
	ctx := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
	out := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, out)
	if out["traceparent"] != carrier["traceparent"] || out["baggage"] != "tenant=w1" {
		t.Fatalf("round trip lost context: %v", out)
	}
}

func TestMetricsAreReadFromTheConfiguredReader(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	tel, err := Setup(context.Background(), Config{
		Service: "svc", Lookup: env(map[string]string{"OTEL_TRACES_EXPORTER": "none", "OTEL_LOGS_EXPORTER": "none"}),
		MetricReader: reader,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tel.Shutdown(context.Background()) }()
	counter, _ := tel.MeterProvider.Meter("t").Int64Counter("skein.test")
	counter.Add(context.Background(), 2)
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	if len(rm.ScopeMetrics) != 1 || rm.ScopeMetrics[0].Metrics[0].Name != "skein.test" {
		t.Fatalf("metric not collected: %+v", rm)
	}
}

func TestLogHandlerExportsRecordsWithTraceContext(t *testing.T) {
	logs := &memoryLogExporter{}
	spans := tracetest.NewInMemoryExporter()
	tel, err := Setup(context.Background(), Config{
		Service: "svc", Lookup: env(map[string]string{"OTEL_METRICS_EXPORTER": "none"}),
		SpanExporter: spans, LogExporter: logs,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := tel.LogHandler()
	if h == nil {
		t.Fatal("logs enabled but no handler")
	}
	ctx, span := tel.TracerProvider.Tracer("t").Start(context.Background(), "op")
	r := newRecord("hello")
	if err := h.Handle(ctx, r); err != nil {
		t.Fatal(err)
	}
	span.End()
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(logs.records) != 1 || logs.records[0].TraceID() != span.SpanContext().TraceID() {
		t.Fatalf("want one record carrying the span's trace id, got %d", len(logs.records))
	}
}

func TestUnsupportedExporterIsRejected(t *testing.T) {
	_, err := Setup(context.Background(), Config{Lookup: env(map[string]string{"OTEL_TRACES_EXPORTER": "zipkin"})})
	if !errors.Is(err, ErrUnsupportedExporter) {
		t.Fatalf("want ErrUnsupportedExporter, got %v", err)
	}
}

func newRecord(msg string) slog.Record { return slog.NewRecord(time.Now(), slog.LevelInfo, msg, 0) }
