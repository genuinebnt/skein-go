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

class SearchMode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    SEARCH_MODE_UNSPECIFIED: _ClassVar[SearchMode]
    SEARCH_MODE_TEXT: _ClassVar[SearchMode]
    SEARCH_MODE_REGEX: _ClassVar[SearchMode]

class BlastDirection(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    BLAST_DIRECTION_UNSPECIFIED: _ClassVar[BlastDirection]
    BLAST_DIRECTION_CITED_BY: _ClassVar[BlastDirection]
    BLAST_DIRECTION_CITES: _ClassVar[BlastDirection]

class PathMetric(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    PATH_METRIC_UNSPECIFIED: _ClassVar[PathMetric]
    PATH_METRIC_HOPS: _ClassVar[PathMetric]
    PATH_METRIC_SEMANTIC: _ClassVar[PathMetric]
SEARCH_MODE_UNSPECIFIED: SearchMode
SEARCH_MODE_TEXT: SearchMode
SEARCH_MODE_REGEX: SearchMode
BLAST_DIRECTION_UNSPECIFIED: BlastDirection
BLAST_DIRECTION_CITED_BY: BlastDirection
BLAST_DIRECTION_CITES: BlastDirection
PATH_METRIC_UNSPECIFIED: PathMetric
PATH_METRIC_HOPS: PathMetric
PATH_METRIC_SEMANTIC: PathMetric

class SearchRequest(_message.Message):
    __slots__ = ("workspace_id", "query", "mode", "case_sensitive", "whole_word", "languages", "code_only", "page")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    QUERY_FIELD_NUMBER: _ClassVar[int]
    MODE_FIELD_NUMBER: _ClassVar[int]
    CASE_SENSITIVE_FIELD_NUMBER: _ClassVar[int]
    WHOLE_WORD_FIELD_NUMBER: _ClassVar[int]
    LANGUAGES_FIELD_NUMBER: _ClassVar[int]
    CODE_ONLY_FIELD_NUMBER: _ClassVar[int]
    PAGE_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    query: str
    mode: SearchMode
    case_sensitive: bool
    whole_word: bool
    languages: _containers.RepeatedScalarFieldContainer[str]
    code_only: bool
    page: _common_pb2.PageRequest
    def __init__(self, workspace_id: _Optional[str] = ..., query: _Optional[str] = ..., mode: _Optional[_Union[SearchMode, str]] = ..., case_sensitive: _Optional[bool] = ..., whole_word: _Optional[bool] = ..., languages: _Optional[_Iterable[str]] = ..., code_only: _Optional[bool] = ..., page: _Optional[_Union[_common_pb2.PageRequest, _Mapping]] = ...) -> None: ...

class SearchResponse(_message.Message):
    __slots__ = ("hits", "page", "candidates", "matched")
    HITS_FIELD_NUMBER: _ClassVar[int]
    PAGE_FIELD_NUMBER: _ClassVar[int]
    CANDIDATES_FIELD_NUMBER: _ClassVar[int]
    MATCHED_FIELD_NUMBER: _ClassVar[int]
    hits: _containers.RepeatedCompositeFieldContainer[SearchHit]
    page: _common_pb2.PageResponse
    candidates: int
    matched: int
    def __init__(self, hits: _Optional[_Iterable[_Union[SearchHit, _Mapping]]] = ..., page: _Optional[_Union[_common_pb2.PageResponse, _Mapping]] = ..., candidates: _Optional[int] = ..., matched: _Optional[int] = ...) -> None: ...

class SearchHit(_message.Message):
    __slots__ = ("location", "note_title", "snippet", "highlights", "score", "language")
    LOCATION_FIELD_NUMBER: _ClassVar[int]
    NOTE_TITLE_FIELD_NUMBER: _ClassVar[int]
    SNIPPET_FIELD_NUMBER: _ClassVar[int]
    HIGHLIGHTS_FIELD_NUMBER: _ClassVar[int]
    SCORE_FIELD_NUMBER: _ClassVar[int]
    LANGUAGE_FIELD_NUMBER: _ClassVar[int]
    location: _common_pb2.BlockRef
    note_title: str
    snippet: str
    highlights: _containers.RepeatedCompositeFieldContainer[Range]
    score: float
    language: str
    def __init__(self, location: _Optional[_Union[_common_pb2.BlockRef, _Mapping]] = ..., note_title: _Optional[str] = ..., snippet: _Optional[str] = ..., highlights: _Optional[_Iterable[_Union[Range, _Mapping]]] = ..., score: _Optional[float] = ..., language: _Optional[str] = ...) -> None: ...

class Range(_message.Message):
    __slots__ = ("start", "end")
    START_FIELD_NUMBER: _ClassVar[int]
    END_FIELD_NUMBER: _ClassVar[int]
    start: int
    end: int
    def __init__(self, start: _Optional[int] = ..., end: _Optional[int] = ...) -> None: ...

class SuggestTitlesRequest(_message.Message):
    __slots__ = ("workspace_id", "prefix", "limit")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    PREFIX_FIELD_NUMBER: _ClassVar[int]
    LIMIT_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    prefix: str
    limit: int
    def __init__(self, workspace_id: _Optional[str] = ..., prefix: _Optional[str] = ..., limit: _Optional[int] = ...) -> None: ...

class SuggestTitlesResponse(_message.Message):
    __slots__ = ("suggestions",)
    SUGGESTIONS_FIELD_NUMBER: _ClassVar[int]
    suggestions: _containers.RepeatedCompositeFieldContainer[TitleSuggestion]
    def __init__(self, suggestions: _Optional[_Iterable[_Union[TitleSuggestion, _Mapping]]] = ...) -> None: ...

class TitleSuggestion(_message.Message):
    __slots__ = ("note_id", "title", "edit_distance")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    EDIT_DISTANCE_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    title: str
    edit_distance: int
    def __init__(self, note_id: _Optional[str] = ..., title: _Optional[str] = ..., edit_distance: _Optional[int] = ...) -> None: ...

class GraphNode(_message.Message):
    __slots__ = ("note_id", "title", "is_root")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    IS_ROOT_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    title: str
    is_root: bool
    def __init__(self, note_id: _Optional[str] = ..., title: _Optional[str] = ..., is_root: _Optional[bool] = ...) -> None: ...

class GraphEdge(_message.Message):
    __slots__ = ("to",)
    FROM_FIELD_NUMBER: _ClassVar[int]
    TO_FIELD_NUMBER: _ClassVar[int]
    to: str
    def __init__(self, to: _Optional[str] = ..., **kwargs) -> None: ...

class GetLinkGraphRequest(_message.Message):
    __slots__ = ("workspace_id",)
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    def __init__(self, workspace_id: _Optional[str] = ...) -> None: ...

class GetLinkGraphResponse(_message.Message):
    __slots__ = ("nodes", "edges")
    NODES_FIELD_NUMBER: _ClassVar[int]
    EDGES_FIELD_NUMBER: _ClassVar[int]
    nodes: _containers.RepeatedCompositeFieldContainer[GraphNode]
    edges: _containers.RepeatedCompositeFieldContainer[GraphEdge]
    def __init__(self, nodes: _Optional[_Iterable[_Union[GraphNode, _Mapping]]] = ..., edges: _Optional[_Iterable[_Union[GraphEdge, _Mapping]]] = ...) -> None: ...

class AnalyzeGraphRequest(_message.Message):
    __slots__ = ("workspace_id",)
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    def __init__(self, workspace_id: _Optional[str] = ...) -> None: ...

class AnalyzeGraphResponse(_message.Message):
    __slots__ = ("component_of", "orphan_components", "scc_of", "scc_sizes", "cycle", "topological_order", "unplaced", "betweenness", "pagerank", "community_of", "modularity", "diameter")
    class ComponentOfEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    class SccOfEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    class BetweennessEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: float
        def __init__(self, key: _Optional[str] = ..., value: _Optional[float] = ...) -> None: ...
    class PagerankEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: float
        def __init__(self, key: _Optional[str] = ..., value: _Optional[float] = ...) -> None: ...
    class CommunityOfEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    COMPONENT_OF_FIELD_NUMBER: _ClassVar[int]
    ORPHAN_COMPONENTS_FIELD_NUMBER: _ClassVar[int]
    SCC_OF_FIELD_NUMBER: _ClassVar[int]
    SCC_SIZES_FIELD_NUMBER: _ClassVar[int]
    CYCLE_FIELD_NUMBER: _ClassVar[int]
    TOPOLOGICAL_ORDER_FIELD_NUMBER: _ClassVar[int]
    UNPLACED_FIELD_NUMBER: _ClassVar[int]
    BETWEENNESS_FIELD_NUMBER: _ClassVar[int]
    PAGERANK_FIELD_NUMBER: _ClassVar[int]
    COMMUNITY_OF_FIELD_NUMBER: _ClassVar[int]
    MODULARITY_FIELD_NUMBER: _ClassVar[int]
    DIAMETER_FIELD_NUMBER: _ClassVar[int]
    component_of: _containers.ScalarMap[str, int]
    orphan_components: _containers.RepeatedScalarFieldContainer[int]
    scc_of: _containers.ScalarMap[str, int]
    scc_sizes: _containers.RepeatedScalarFieldContainer[int]
    cycle: _containers.RepeatedScalarFieldContainer[str]
    topological_order: _containers.RepeatedScalarFieldContainer[str]
    unplaced: _containers.RepeatedScalarFieldContainer[str]
    betweenness: _containers.ScalarMap[str, float]
    pagerank: _containers.ScalarMap[str, float]
    community_of: _containers.ScalarMap[str, int]
    modularity: float
    diameter: int
    def __init__(self, component_of: _Optional[_Mapping[str, int]] = ..., orphan_components: _Optional[_Iterable[int]] = ..., scc_of: _Optional[_Mapping[str, int]] = ..., scc_sizes: _Optional[_Iterable[int]] = ..., cycle: _Optional[_Iterable[str]] = ..., topological_order: _Optional[_Iterable[str]] = ..., unplaced: _Optional[_Iterable[str]] = ..., betweenness: _Optional[_Mapping[str, float]] = ..., pagerank: _Optional[_Mapping[str, float]] = ..., community_of: _Optional[_Mapping[str, int]] = ..., modularity: _Optional[float] = ..., diameter: _Optional[int] = ...) -> None: ...

class BlastRadiusRequest(_message.Message):
    __slots__ = ("note_id", "max_hops", "direction")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    MAX_HOPS_FIELD_NUMBER: _ClassVar[int]
    DIRECTION_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    max_hops: int
    direction: BlastDirection
    def __init__(self, note_id: _Optional[str] = ..., max_hops: _Optional[int] = ..., direction: _Optional[_Union[BlastDirection, str]] = ...) -> None: ...

class BlastRadiusResponse(_message.Message):
    __slots__ = ("hops", "ring_sizes")
    class HopsEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    HOPS_FIELD_NUMBER: _ClassVar[int]
    RING_SIZES_FIELD_NUMBER: _ClassVar[int]
    hops: _containers.ScalarMap[str, int]
    ring_sizes: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, hops: _Optional[_Mapping[str, int]] = ..., ring_sizes: _Optional[_Iterable[int]] = ...) -> None: ...

class ShortestPathRequest(_message.Message):
    __slots__ = ("to", "metric")
    FROM_FIELD_NUMBER: _ClassVar[int]
    TO_FIELD_NUMBER: _ClassVar[int]
    METRIC_FIELD_NUMBER: _ClassVar[int]
    to: str
    metric: PathMetric
    def __init__(self, to: _Optional[str] = ..., metric: _Optional[_Union[PathMetric, str]] = ..., **kwargs) -> None: ...

class ShortestPathResponse(_message.Message):
    __slots__ = ("found", "path", "cost")
    FOUND_FIELD_NUMBER: _ClassVar[int]
    PATH_FIELD_NUMBER: _ClassVar[int]
    COST_FIELD_NUMBER: _ClassVar[int]
    found: bool
    path: _containers.RepeatedScalarFieldContainer[str]
    cost: float
    def __init__(self, found: _Optional[bool] = ..., path: _Optional[_Iterable[str]] = ..., cost: _Optional[float] = ...) -> None: ...

class ReadingPathRequest(_message.Message):
    __slots__ = ("note_id",)
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    def __init__(self, note_id: _Optional[str] = ...) -> None: ...

class ReadingPathResponse(_message.Message):
    __slots__ = ("steps",)
    STEPS_FIELD_NUMBER: _ClassVar[int]
    steps: _containers.RepeatedCompositeFieldContainer[ReadingStep]
    def __init__(self, steps: _Optional[_Iterable[_Union[ReadingStep, _Mapping]]] = ...) -> None: ...

class ReadingStep(_message.Message):
    __slots__ = ("note_id", "reason", "depth")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    DEPTH_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    reason: str
    depth: int
    def __init__(self, note_id: _Optional[str] = ..., reason: _Optional[str] = ..., depth: _Optional[int] = ...) -> None: ...

class ReadingPlanRequest(_message.Message):
    __slots__ = ("workspace_id", "topic", "minutes")
    WORKSPACE_ID_FIELD_NUMBER: _ClassVar[int]
    TOPIC_FIELD_NUMBER: _ClassVar[int]
    MINUTES_FIELD_NUMBER: _ClassVar[int]
    workspace_id: str
    topic: str
    minutes: int
    def __init__(self, workspace_id: _Optional[str] = ..., topic: _Optional[str] = ..., minutes: _Optional[int] = ...) -> None: ...

class ReadingPlanResponse(_message.Message):
    __slots__ = ("notes", "total_minutes", "total_value")
    NOTES_FIELD_NUMBER: _ClassVar[int]
    TOTAL_MINUTES_FIELD_NUMBER: _ClassVar[int]
    TOTAL_VALUE_FIELD_NUMBER: _ClassVar[int]
    notes: _containers.RepeatedCompositeFieldContainer[PlannedNote]
    total_minutes: int
    total_value: float
    def __init__(self, notes: _Optional[_Iterable[_Union[PlannedNote, _Mapping]]] = ..., total_minutes: _Optional[int] = ..., total_value: _Optional[float] = ...) -> None: ...

class PlannedNote(_message.Message):
    __slots__ = ("note_id", "minutes", "value", "reason")
    NOTE_ID_FIELD_NUMBER: _ClassVar[int]
    MINUTES_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    note_id: str
    minutes: int
    value: float
    reason: str
    def __init__(self, note_id: _Optional[str] = ..., minutes: _Optional[int] = ..., value: _Optional[float] = ..., reason: _Optional[str] = ...) -> None: ...
