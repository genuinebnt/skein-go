package app

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// metrics are the documents service's own instruments, beside the RPC
// metrics grpcx records (docs/observability.md). Attributes take a few
// fixed values; note and user IDs never appear.
//
//	skein.documents.actors              up-down counter  open note actors
//	skein.documents.sync.sessions       up-down counter  open sync streams
//	skein.documents.ops.applied         counter          ops persisted, by skein.batch.kind
//	skein.documents.batches.rejected    counter          refused client batches, by skein.reject.reason
//	skein.documents.actor.load.duration histogram (s)    loading a note (snapshot plus replay)
type metrics struct {
	actors   metric.Int64UpDownCounter
	sessions metric.Int64UpDownCounter
	applied  metric.Int64Counter
	rejected metric.Int64Counter
	load     metric.Float64Histogram
}

// requestBuckets is the observability contract's request-duration advice.
var requestBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

func newMetrics(mp metric.MeterProvider) *metrics {
	if mp == nil {
		mp = otel.GetMeterProvider()
	}
	meter := mp.Meter("github.com/genuinebnt/skein-go/services/documents")
	m := &metrics{}
	// Instrument creation only fails on invalid names, which these are not;
	// a failed instrument is a no-op, never a crash.
	m.actors, _ = meter.Int64UpDownCounter("skein.documents.actors", metric.WithUnit("{actor}"), metric.WithDescription("Open note actors."))
	m.sessions, _ = meter.Int64UpDownCounter("skein.documents.sync.sessions", metric.WithUnit("{session}"), metric.WithDescription("Open sync streams."))
	m.applied, _ = meter.Int64Counter("skein.documents.ops.applied", metric.WithUnit("{op}"), metric.WithDescription("CRDT operations persisted to the op log."))
	m.rejected, _ = meter.Int64Counter("skein.documents.batches.rejected", metric.WithUnit("{batch}"), metric.WithDescription("Client batches refused, with a SyncRejected or an error."))
	m.load, _ = meter.Float64Histogram("skein.documents.actor.load.duration", metric.WithUnit("s"),
		metric.WithDescription("Time to load a note from its snapshot and op log."), metric.WithExplicitBucketBoundaries(requestBuckets...))
	return m
}

func (m *metrics) reject(ctx context.Context, reason string) {
	m.rejected.Add(ctx, 1, metric.WithAttributes(attribute.String("skein.reject.reason", reason)))
}

func (m *metrics) loaded(ctx context.Context, since time.Time) {
	m.load.Record(ctx, time.Since(since).Seconds())
}
