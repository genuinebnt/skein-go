"""From-scratch vector search: exact scan, IVF and HNSW (ADR-0010, ADR-0015).

All indexes implement `VectorIndex` over cosine similarity on float32 unit
vectors and accept a boolean ``allowed`` mask, so permission and tag filters
are applied inside the search rather than after it.
"""

from .brute_force import BruteForceIndex
from .common import Hits, VectorIndex, normalize, recall_at_k, top_k
from .hnsw import HNSWIndex
from .ivf import IVFIndex

__all__ = [
    "BruteForceIndex",
    "HNSWIndex",
    "Hits",
    "IVFIndex",
    "VectorIndex",
    "normalize",
    "recall_at_k",
    "top_k",
]
