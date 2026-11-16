"""The gRPC adapter: translation and error mapping, then the real server over loopback."""

from __future__ import annotations

import grpc
import pytest
from opentelemetry import metrics, trace
from tests_support import make_app

from skein_ml.adapters.grpc_server import SERVICE_NAME, MLServicer, build_server
from skein_ml.adapters.grpc_stubs import ml_pb2, ml_pb2_grpc
from skein_ml.adapters.observability import ServiceMetrics
from skein_ml.app.events import BlocksChanged, ChangedBlock, Event, LinksChanged, NoteCreated


class Aborted(Exception):
    pass


class FakeContext:
    """The parts of ``grpc.ServicerContext`` the servicer uses."""

    def __init__(self, remaining: float | None = None):
        self.code = None
        self.details = ""
        self.remaining = remaining

    def abort(self, code, details):
        self.code, self.details = code, details
        raise Aborted(details)

    def time_remaining(self):
        return self.remaining


@pytest.fixture
def servicer() -> MLServicer:
    app = make_app()
    events = [
        NoteCreated("a", "ws", "A"),
        NoteCreated("b", "ws", "B"),
        BlocksChanged("a", (ChangedBlock("a:1", "write ahead log #db"),)),
        BlocksChanged("b", (ChangedBlock("b:1", "write ahead log recovery #db"),)),
        LinksChanged("a", ("c",)),
    ]
    for i, payload in enumerate(events):
        app.handle(Event(f"e{i}", payload))
    return MLServicer(app)


def test_semantic_search_translates_hits_and_recall(servicer):
    request = ml_pb2.SemanticSearchRequest(
        workspace_id="ws",
        query="write ahead log",
        k=2,
        index=ml_pb2.INDEX_KIND_HNSW,
        measure_recall=True,
    )
    response = servicer.SemanticSearch(request, FakeContext())
    assert {h.location.note_id for h in response.hits} == {"a", "b"}
    assert response.hits[0].location.block_id in {"a:1", "b:1"}
    assert response.recall_at_k == 1.0


def test_discover_returns_the_three_signals(servicer):
    response = servicer.Discover(ml_pb2.DiscoverRequest(note_id="a", k=5), FakeContext())
    (related,) = response.related
    assert related.note_id == "b"
    assert related.hops == -1 and related.shared_tags == 1 and related.tag_jaccard == 1.0
    assert related.suggest_link == (related.cosine >= 0.70)


@pytest.mark.parametrize(
    ("call", "request_message", "code"),
    [
        ("Discover", ml_pb2.DiscoverRequest(note_id="missing"), grpc.StatusCode.NOT_FOUND),
        (
            "SemanticSearch",
            ml_pb2.SemanticSearchRequest(workspace_id="ws", query=""),
            grpc.StatusCode.INVALID_ARGUMENT,
        ),
        ("ListClusters", ml_pb2.ListClustersRequest(), grpc.StatusCode.INVALID_ARGUMENT),
        (
            "BenchmarkClustering",
            ml_pb2.BenchmarkClusteringRequest(
                workspace_id="ws", algorithms=[ml_pb2.CLUSTERING_ALGORITHM_UNSPECIFIED]
            ),
            grpc.StatusCode.INVALID_ARGUMENT,
        ),
    ],
)
def test_app_errors_map_to_status_codes(servicer, call, request_message, code):
    context = FakeContext()
    with pytest.raises(Aborted):
        getattr(servicer, call)(request_message, context)
    assert context.code == code


def test_benchmark_stops_when_the_deadline_has_passed(servicer):
    request = ml_pb2.BenchmarkClusteringRequest(
        workspace_id="ws", algorithms=[ml_pb2.CLUSTERING_ALGORITHM_KMEANS_PLUS_PLUS], k=1
    )
    context = FakeContext(remaining=0.0)
    with pytest.raises(Aborted):
        servicer.BenchmarkClustering(request, context)
    assert context.code == grpc.StatusCode.DEADLINE_EXCEEDED


def test_benchmark_results_use_proto_objective_units(servicer):
    request = ml_pb2.BenchmarkClusteringRequest(
        workspace_id="ws",
        k=1,
        runs=1,
        seed=3,
        algorithms=[
            ml_pb2.CLUSTERING_ALGORITHM_KMEANS_PLUS_PLUS,
            ml_pb2.CLUSTERING_ALGORITHM_GMM_EM,
        ],
    )
    results = servicer.BenchmarkClustering(request, FakeContext()).results
    scratch = [r for r in results if r.implementation == "scratch"]
    assert [r.algorithm for r in scratch] == [
        ml_pb2.CLUSTERING_ALGORITHM_KMEANS_PLUS_PLUS,
        ml_pb2.CLUSTERING_ALGORITHM_GMM_EM,
    ]
    assert all(r.epochs >= 1 for r in scratch)


@pytest.mark.loopback
def test_real_server_serves_health_propagates_request_ids_and_drains(servicer):
    from grpc_health.v1 import health_pb2, health_pb2_grpc

    server, health, port = build_server(
        servicer.app,
        "127.0.0.1:0",
        trace.get_tracer("t"),
        ServiceMetrics(metrics.get_meter("t")),
        max_workers=2,
    )
    server.start()
    try:
        with grpc.insecure_channel(f"127.0.0.1:{port}") as channel:
            status = health_pb2_grpc.HealthStub(channel).Check(
                health_pb2.HealthCheckRequest(service=SERVICE_NAME), timeout=5
            )
            assert status.status == health_pb2.HealthCheckResponse.SERVING
            stub = ml_pb2_grpc.MLServiceStub(channel)
            response, call = stub.SemanticSearch.with_call(
                ml_pb2.SemanticSearchRequest(workspace_id="ws", query="log", k=1),
                metadata=[("x-request-id", "req-123")],
                timeout=5,
            )
            assert len(response.hits) == 1
            assert ("x-request-id", "req-123") in call.initial_metadata()
            with pytest.raises(grpc.RpcError) as failure:
                stub.Discover(ml_pb2.DiscoverRequest(note_id="nope"), timeout=5)
            assert failure.value.code() == grpc.StatusCode.NOT_FOUND
            health.enter_graceful_shutdown()
            status = health_pb2_grpc.HealthStub(channel).Check(
                health_pb2.HealthCheckRequest(service=SERVICE_NAME), timeout=5
            )
            assert status.status == health_pb2.HealthCheckResponse.NOT_SERVING
    finally:
        server.stop(1).wait()
