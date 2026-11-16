from google.api import annotations_pb2 as _annotations_pb2
from google.api import field_behavior_pb2 as _field_behavior_pb2
from skein_proto.protoc_gen_openapiv2.options import annotations_pb2 as _annotations_pb2_1
from skein_proto.v1 import common_pb2 as _common_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class IndexKind(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    INDEX_KIND_UNSPECIFIED: _ClassVar[IndexKind]
    INDEX_KIND_BRUTE_FORCE: _ClassVar[IndexKind]
    INDEX_KIND_IVF: _ClassVar[IndexKind]
    INDEX_KIND_HNSW: _ClassVar[IndexKind]

class ClusteringAlgorithm(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    CLUSTERING_ALGORITHM_UNSPECIFIED: _ClassVar[ClusteringAlgorithm]
    CLUSTERING_ALGORITHM_KMEANS_RANDOM: _ClassVar[ClusteringAlgorithm]
    CLUSTERING_ALGORITHM_KMEANS_PLUS_PLUS: _ClassVar[ClusteringAlgorithm]
    CLUSTERING_ALGORITHM_KMEANS_ELKAN: _ClassVar[ClusteringAlgorithm]
    CLUSTERING_ALGORITHM_KMEANS_MINI_BATCH: _ClassVar[ClusteringAlgorithm]
    CLUSTERING_ALGORITHM_GMM_EM: _ClassVar[ClusteringAlgorithm]
    CLUSTERING_ALGORITHM_E_MEANS: _ClassVar[ClusteringAlgorithm]
INDEX_KIND_UNSPECIFIED: IndexKind
INDEX_KIND_BRUTE_FORCE: IndexKind
INDEX_KIND_IVF: IndexKind
INDEX_KIND_HNSW: IndexKind
CLUSTERING_ALGORITHM_UNSPECIFIED: ClusteringAlgorithm
CLUSTERING_ALGORITHM_KMEANS_RANDOM: ClusteringAlgorithm
CLUSTERING_ALGORITHM_KMEANS_PLUS_PLUS: ClusteringAlgorithm
CLUSTERING_ALGORITHM_KMEANS_ELKAN: ClusteringAlgorithm
CLUSTERING_ALGORITHM_KMEANS_MINI_BATCH: ClusteringAlgorithm
CLUSTERING_ALGORITHM_GMM_EM: ClusteringAlgorithm
CLUSTERING_ALGORITHM_E_MEANS: ClusteringAlgorithm

class SemanticSearchRequest(_message.Message):
    __slots__ = ("workspace_id", "query", "k", "index", "tags", "measure_recall")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    QUERY_FIELD_NUMBER: _ClassVar[int]
    K_FIELD_NUMBER: _ClassVar[int]
    INDEX_FIELD_NUMBER: _ClassVar[int]
    TAGS_FIELD_NUMBER: _ClassVar[int]
    MEASURE_RECALL_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    query: str
    k: int
    index: IndexKind
    tags: _containers.RepeatedScalarFieldContainer[str]
    measure_recall: bool
    def __init__(self, workspace_id: _Optional[str] = ..., query: _Optional[str] = ..., k: _Optional[int] = ..., index: _Optional[_Union[IndexKind, str]] = ..., tags: _Optional[_Iterable[str]] = ..., measure_recall: _Optional[bool] = ...) -> None: ...

class SemanticSearchResponse(_message.Message):
    __slots__ = ("hits", "recall_at_k", "latency_ms")
    HITS_FIELD_NUMBER: _ClassVar[int]
    RECALL_AT_K_FIELD_NUMBER: _ClassVar[int]
    LATENCY_MS_FIELD_NUMBER: _ClassVar[int]
    hits: _containers.RepeatedCompositeFieldContainer[SemanticHit]
    recall_at_k: float
    latency_ms: float
    def __init__(self, hits: _Optional[_Iterable[_Union[SemanticHit, _Mapping]]] = ..., recall_at_k: _Optional[float] = ..., latency_ms: _Optional[float] = ...) -> None: ...

class SemanticHit(_message.Message):
    __slots__ = ("location", "cosine")
    LOCATION_FIELD_NUMBER: _ClassVar[int]
    COSINE_FIELD_NUMBER: _ClassVar[int]
    location: _common_pb2.BlockRef
    cosine: float
    def __init__(self, location: _Optional[_Union[_common_pb2.BlockRef, _Mapping]] = ..., cosine: _Optional[float] = ...) -> None: ...

class DiscoverRequest(_message.Message):
    __slots__ = ("note_id", "k")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    K_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    k: int
    def __init__(self, note_id: _Optional[str] = ..., k: _Optional[int] = ...) -> None: ...

class DiscoverResponse(_message.Message):
    __slots__ = ("related",)
    RELATED_FIELD_NUMBER: _ClassVar[int]
    related: _containers.RepeatedCompositeFieldContainer[RelatedNote]
    def __init__(self, related: _Optional[_Iterable[_Union[RelatedNote, _Mapping]]] = ...) -> None: ...

class RelatedNote(_message.Message):
    __slots__ = ("note_id", "cosine", "shared_tags", "tag_jaccard", "hops", "suggest_link")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    COSINE_FIELD_NUMBER: _ClassVar[int]
    SHARED_TAGS_FIELD_NUMBER: _ClassVar[int]
    TAG_JACCARD_FIELD_NUMBER: _ClassVar[int]
    HOPS_FIELD_NUMBER: _ClassVar[int]
    SUGGEST_LINK_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    cosine: float
    shared_tags: int
    tag_jaccard: float
    hops: int
    suggest_link: bool
    def __init__(self, note_id: _Optional[str] = ..., cosine: _Optional[float] = ..., shared_tags: _Optional[int] = ..., tag_jaccard: _Optional[float] = ..., hops: _Optional[int] = ..., suggest_link: _Optional[bool] = ...) -> None: ...

class ListClustersRequest(_message.Message):
    __slots__ = ("workspace_id",)
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    def __init__(self, workspace_id: _Optional[str] = ...) -> None: ...

class ListClustersResponse(_message.Message):
    __slots__ = ("clusters",)
    CLUSTERS_FIELD_NUMBER: _ClassVar[int]
    clusters: _containers.RepeatedCompositeFieldContainer[Cluster]
    def __init__(self, clusters: _Optional[_Iterable[_Union[Cluster, _Mapping]]] = ...) -> None: ...

class Cluster(_message.Message):
    __slots__ = ("id", "label", "top_terms", "note_ids")
    ID_FIELD_NUMBER: _ClassVar[int]
    LABEL_FIELD_NUMBER: _ClassVar[int]
    TOP_TERMS_FIELD_NUMBER: _ClassVar[int]
    NOTE_IDS_FIELD_NUMBER: _ClassVar[int]
    id: int
    label: str
    top_terms: _containers.RepeatedScalarFieldContainer[str]
    note_ids: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, id: _Optional[int] = ..., label: _Optional[str] = ..., top_terms: _Optional[_Iterable[str]] = ..., note_ids: _Optional[_Iterable[str]] = ...) -> None: ...

class BenchmarkClusteringRequest(_message.Message):
    __slots__ = ("workspace_id", "algorithms", "k", "runs", "seed")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    ALGORITHMS_FIELD_NUMBER: _ClassVar[int]
    K_FIELD_NUMBER: _ClassVar[int]
    RUNS_FIELD_NUMBER: _ClassVar[int]
    SEED_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    algorithms: _containers.RepeatedScalarFieldContainer[ClusteringAlgorithm]
    k: int
    runs: int
    seed: int
    def __init__(self, workspace_id: _Optional[str] = ..., algorithms: _Optional[_Iterable[_Union[ClusteringAlgorithm, str]]] = ..., k: _Optional[int] = ..., runs: _Optional[int] = ..., seed: _Optional[int] = ...) -> None: ...

class BenchmarkClusteringResponse(_message.Message):
    __slots__ = ("results",)
    RESULTS_FIELD_NUMBER: _ClassVar[int]
    results: _containers.RepeatedCompositeFieldContainer[ClusteringResult]
    def __init__(self, results: _Optional[_Iterable[_Union[ClusteringResult, _Mapping]]] = ...) -> None: ...

class ClusteringResult(_message.Message):
    __slots__ = ("algorithm", "implementation", "mean_seconds", "mean_iterations", "objective", "silhouette", "distance_computations", "epochs", "test_objective", "dead_clusters")
    ALGORITHM_FIELD_NUMBER: _ClassVar[int]
    IMPLEMENTATION_FIELD_NUMBER: _ClassVar[int]
    MEAN_SECONDS_FIELD_NUMBER: _ClassVar[int]
    MEAN_ITERATIONS_FIELD_NUMBER: _ClassVar[int]
    OBJECTIVE_FIELD_NUMBER: _ClassVar[int]
    SILHOUETTE_FIELD_NUMBER: _ClassVar[int]
    DISTANCE_COMPUTATIONS_FIELD_NUMBER: _ClassVar[int]
    EPOCHS_FIELD_NUMBER: _ClassVar[int]
    TEST_OBJECTIVE_FIELD_NUMBER: _ClassVar[int]
    DEAD_CLUSTERS_FIELD_NUMBER: _ClassVar[int]
    algorithm: ClusteringAlgorithm
    implementation: str
    mean_seconds: float
    mean_iterations: float
    objective: float
    silhouette: float
    distance_computations: int
    epochs: float
    test_objective: float
    dead_clusters: int
    def __init__(self, algorithm: _Optional[_Union[ClusteringAlgorithm, str]] = ..., implementation: _Optional[str] = ..., mean_seconds: _Optional[float] = ..., mean_iterations: _Optional[float] = ..., objective: _Optional[float] = ..., silhouette: _Optional[float] = ..., distance_computations: _Optional[int] = ..., epochs: _Optional[float] = ..., test_objective: _Optional[float] = ..., dead_clusters: _Optional[int] = ...) -> None: ...
