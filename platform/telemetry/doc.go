// Package telemetry sets up OpenTelemetry traces, metrics and logs for a
// service and returns one Shutdown function that flushes all three.
//
// # Configuration
//
// Everything is configured by the standard OpenTelemetry environment
// variables, so the same image runs against the local collector, kind and
// Cloud Trace without code changes:
//
//	OTEL_SDK_DISABLED=true             no-op providers; nothing is exported
//	OTEL_EXPORTER_OTLP_ENDPOINT        collector address (default localhost:4317)
//	OTEL_EXPORTER_OTLP_INSECURE        plaintext gRPC to a local collector
//	OTEL_EXPORTER_OTLP_HEADERS         extra headers, e.g. auth for a SaaS backend
//	OTEL_TRACES_EXPORTER / OTEL_METRICS_EXPORTER / OTEL_LOGS_EXPORTER
//	                                   "otlp" (default) or "none" per signal
//	OTEL_TRACES_SAMPLER(_ARG)          sampling, read by the SDK
//	OTEL_METRIC_EXPORT_INTERVAL        push interval, read by the SDK
//	OTEL_SERVICE_NAME, OTEL_RESOURCE_ATTRIBUTES
//	                                   override or extend the resource
//
// The per-signal variants (OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, ...) work
// too, because the exporters read them directly. Only the OTLP gRPC
// protocol is built in; OTEL_EXPORTER_OTLP_PROTOCOL is ignored.
//
// The resource carries service.name, service.version and
// deployment.environment.name from the Config, plus whatever the
// environment adds; environment values win, as the specification says.
//
// # Globals
//
// Setup installs the providers and the W3C propagator (traceparent,
// tracestate and baggage) as the OpenTelemetry globals. That is the one
// global this module sets on purpose: the instrumentation libraries
// (otelgrpc, otelhttp, otelpgx, redisotel) read them, and every service has
// exactly one telemetry setup. Code in this module that needs a provider
// takes it as an option and falls back to the global.
//
// # RED metrics
//
// Request rate, errors and duration come from the instrumentation libraries
// wired up by grpcx and httpx, which follow the semantic conventions:
//
//	rpc.server.duration, rpc.client.duration (ms)   by rpc.service, rpc.method,
//	                                                rpc.grpc.status_code
//	http.server.request.duration (s)                by http.request.method,
//	                                                http.route, http.response.status_code
//
// Rate is the histogram count and errors are the counts with an error
// status, so no separate counters are needed. docs/platform.md lists every
// metric the module emits, including the skein.* ones from redisx,
// ratelimit and outbox.
//
// # Logs
//
// LogHandler returns an slog.Handler bridged to the OpenTelemetry logs
// pipeline; pass it to logging.Options.Extra so records are exported with
// their trace context alongside the JSON written to stdout. It is nil when
// logs export is off.
//
// # Porting notes
//
// Rust: `opentelemetry_sdk` with `opentelemetry-otlp` (tonic exporter),
// `tracing-opentelemetry` to bridge spans and
// `opentelemetry-appender-tracing` for logs. The environment variables are
// the same.
package telemetry
