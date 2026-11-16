"""gRPC inbound adapter: ``skein.v1.MLService`` over `MLApp`, plus health.

The servicer only translates: Protobuf in, use case call, Protobuf out.
Typed app errors map to status codes through `_STATUS`; anything else is
INTERNAL and logged with its stack trace (never with request contents).

`ObservabilityInterceptor` wraps every unary handler:

- request id from ``x-request-id`` metadata (the gateway sets it), or a new
  one, echoed back in the initial metadata;
- W3C trace context from ``traceparent``/``tracestate`` metadata, and a
  SERVER span named after the method;
- one ``rpc finished`` log line and the request counter and latency
  histogram, labelled by method and status code.

Deadlines: gRPC enforces them on the wire; long handlers (the clustering
benchmark) also poll ``context.time_remaining()`` and stop early.
"""

from __future__ import annotations

import logging
import time
from concurrent import futures

import grpc
from grpc_health.v1 import health, health_pb2, health_pb2_grpc
from opentelemetry import propagate, trace

from ..app.errors import DeadlineExceeded, InvalidArgument, MLError, NotFound, Unavailable
from ..app.service import MLApp
from ..app.vectors import IndexKind
from .grpc_stubs import ml_pb2, ml_pb2_grpc
from .observability import ServiceMetrics, new_request_id, request_context

__all__ = ["SERVICE_NAME", "MLServicer", "ObservabilityInterceptor", "build_server"]

log = logging.getLogger(__name__)

SERVICE_NAME = "skein.v1.MLService"

_STATUS = {
    NotFound: grpc.StatusCode.NOT_FOUND,
    InvalidArgument: grpc.StatusCode.INVALID_ARGUMENT,
    Unavailable: grpc.StatusCode.UNAVAILABLE,
    DeadlineExceeded: grpc.StatusCode.DEADLINE_EXCEEDED,
}

_INDEX_FROM_PROTO = {
    ml_pb2.INDEX_KIND_UNSPECIFIED: None,
    ml_pb2.INDEX_KIND_BRUTE_FORCE: IndexKind.BRUTE_FORCE,
    ml_pb2.INDEX_KIND_IVF: IndexKind.IVF,
    ml_pb2.INDEX_KIND_HNSW: IndexKind.HNSW,
}

_ALGORITHM_NAMES = {
    ml_pb2.CLUSTERING_ALGORITHM_KMEANS_RANDOM: "kmeans_random",
    ml_pb2.CLUSTERING_ALGORITHM_KMEANS_PLUS_PLUS: "kmeans_plus_plus",
    ml_pb2.CLUSTERING_ALGORITHM_KMEANS_ELKAN: "kmeans_elkan",
    ml_pb2.CLUSTERING_ALGORITHM_KMEANS_MINI_BATCH: "kmeans_mini_batch",
    ml_pb2.CLUSTERING_ALGORITHM_GMM_EM: "gmm_em",
    ml_pb2.CLUSTERING_ALGORITHM_E_MEANS: "e_means",
}
_ALGORITHM_ENUMS = {name: value for value, name in _ALGORITHM_NAMES.items()}


def _fail(context: grpc.ServicerContext, error: Exception):
    code = next((c for kind, c in _STATUS.items() if isinstance(error, kind)), None)
    if code is None:
        log.exception("unhandled error in handler")
        context.abort(grpc.StatusCode.INTERNAL, "internal error")
    context.abort(code, str(error))


class MLServicer(ml_pb2_grpc.MLServiceServicer):
    """Translates MLService RPCs to `MLApp` calls."""

    def __init__(self, app: MLApp):
        self.app = app

    def SemanticSearch(self, request, context):  # noqa: N802 - generated name
        """See ``ml.proto``."""
        try:
            outcome = self.app.semantic_search(
                request.workspace_id, request.query, request.k,
                _INDEX_FROM_PROTO.get(request.index), list(request.tags), request.measure_recall)
        except MLError as error:
            _fail(context, error)
        return ml_pb2.SemanticSearchResponse(
            hits=[ml_pb2.SemanticHit(location={"note_id": h.note_id, "block_id": h.block_id},
                                     cosine=h.cosine) for h in outcome.hits],
            recall_at_k=outcome.recall_at_k, latency_ms=outcome.latency_ms)

    def Discover(self, request, context):  # noqa: N802
        """See ``ml.proto``."""
        try:
            related = self.app.discover(request.note_id, request.k)
        except MLError as error:
            _fail(context, error)
        return ml_pb2.DiscoverResponse(related=[
            ml_pb2.RelatedNote(note_id=r.note_id, cosine=r.cosine, shared_tags=r.shared_tags,
                               tag_jaccard=r.tag_jaccard, hops=r.hops, suggest_link=r.suggest_link)
            for r in related])

    def ListClusters(self, request, context):  # noqa: N802
        """See ``ml.proto``."""
        try:
            clusters = self.app.list_clusters(request.workspace_id)
        except MLError as error:
            _fail(context, error)
        return ml_pb2.ListClustersResponse(clusters=[
            ml_pb2.Cluster(id=c.id, label=c.label, top_terms=c.top_terms, note_ids=c.note_ids)
            for c in clusters])

    def BenchmarkClustering(self, request, context):  # noqa: N802
        """See ``ml.proto``; objectives are sums (K-means) or mean NLL (mixtures)."""
        unknown = [a for a in request.algorithms if a not in _ALGORITHM_NAMES]
        if unknown:
            context.abort(grpc.StatusCode.INVALID_ARGUMENT, f"unsupported algorithms {unknown}")
        names = [_ALGORITHM_NAMES[a] for a in request.algorithms]

        def expired() -> bool:
            remaining = context.time_remaining()
            return remaining is not None and remaining <= 0

        try:
            runs = self.app.benchmark_clustering(request.workspace_id, names, request.k,
                                                 request.runs, request.seed, expired=expired)
        except MLError as error:
            _fail(context, error)
        return ml_pb2.BenchmarkClusteringResponse(results=[_result(r) for r in runs])


def _result(run) -> ml_pb2.ClusteringResult:
    mixture = run.algorithm == "gmm_em"
    objective = run.negative_log_likelihood if mixture else run.train_objective * run.train_points
    test_objective = (run.test_negative_log_likelihood or 0.0) if mixture \
        else run.test_objective * run.test_points
    return ml_pb2.ClusteringResult(
        algorithm=_ALGORITHM_ENUMS[run.algorithm], implementation=run.implementation,
        mean_seconds=run.seconds, mean_iterations=run.iterations, objective=objective,
        silhouette=run.silhouette, distance_computations=run.distance_computations or 0,
        epochs=run.epochs, test_objective=test_objective,
        dead_clusters=round(run.dead_clusters))


class ObservabilityInterceptor(grpc.ServerInterceptor):
    """Request ids, trace context, spans, metrics and access logs for unary RPCs."""

    def __init__(self, tracer: trace.Tracer, service_metrics: ServiceMetrics):
        self._tracer = tracer
        self._metrics = service_metrics

    def intercept_service(self, continuation, handler_call_details):
        """Wrap the unary-unary behaviour of the resolved handler."""
        handler = continuation(handler_call_details)
        if handler is None or handler.unary_unary is None:
            return handler
        method = handler_call_details.method
        inner = handler.unary_unary
        tracer, service_metrics = self._tracer, self._metrics

        def observed(request, context):
            metadata = {k.lower(): v for k, v in (context.invocation_metadata() or ())}
            request_id = metadata.get("x-request-id") or new_request_id()
            parent = propagate.extract(metadata)
            started = time.perf_counter()
            code = grpc.StatusCode.OK
            with request_context(request_id), tracer.start_as_current_span(
                    method.lstrip("/"), context=parent, kind=trace.SpanKind.SERVER,
                    attributes={"rpc.system": "grpc", "rpc.method": method}) as span:
                context.send_initial_metadata((("x-request-id", request_id),))
                try:
                    return inner(request, context)
                except Exception:
                    code = context.code() or grpc.StatusCode.UNKNOWN
                    raise
                finally:
                    elapsed = (time.perf_counter() - started) * 1000.0
                    if code != grpc.StatusCode.OK:
                        span.set_status(trace.Status(trace.StatusCode.ERROR, code.name))
                    labels = {"rpc.method": method, "rpc.grpc.status_code": code.name}
                    service_metrics.rpc_requests.add(1, labels)
                    service_metrics.rpc_duration.record(elapsed, labels)
                    # Health probes arrive every few seconds; keep them out of INFO.
                    level = logging.DEBUG if method.startswith("/grpc.health.") else logging.INFO
                    log.log(level, "rpc finished", extra={"method": method, "code": code.name,
                                                          "duration_ms": round(elapsed, 3)})

        return grpc.unary_unary_rpc_method_handler(
            observed, request_deserializer=handler.request_deserializer,
            response_serializer=handler.response_serializer)


def build_server(app: MLApp, address: str, tracer: trace.Tracer,
                 service_metrics: ServiceMetrics, max_workers: int = 8
                 ) -> tuple[grpc.Server, health.HealthServicer, int]:
    """A started-ready server bound to ``address``; returns (server, health, port)."""
    server = grpc.server(futures.ThreadPoolExecutor(max_workers=max_workers,
                                                    thread_name_prefix="grpc"),
                         interceptors=[ObservabilityInterceptor(tracer, service_metrics)])
    ml_pb2_grpc.add_MLServiceServicer_to_server(MLServicer(app), server)
    health_servicer = health.HealthServicer()
    health_pb2_grpc.add_HealthServicer_to_server(health_servicer, server)
    for name in ("", SERVICE_NAME):
        health_servicer.set(name, health_pb2.HealthCheckResponse.SERVING)
    port = server.add_insecure_port(address)
    return server, health_servicer, port
