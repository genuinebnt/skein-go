"""Hierarchical Navigable Small World graphs (Malkov and Yashunin, 2016/2018).

Structure: every node gets a level ``l = floor(-ln(U) * mL)`` with
``mL = 1 / ln(M)``, so each layer holds roughly ``1/M`` of the layer below.
Layer ``lc`` links each node to at most ``M`` neighbours (``M0 = 2M`` on
layer 0). The entry point is a node on the top layer.

Insertion (paper's Algorithm 1): greedy descent with ``ef = 1`` from the
entry point to layer ``l + 1``; then on each layer from ``min(l, top)`` down
to 0, a beam search with ``ef = efConstruction`` (Algorithm 2) finds
candidates, the *heuristic* (Algorithm 4) picks up to ``M`` diverse
neighbours, links are made in both directions, and any neighbour whose list
overflows is re-pruned with the same heuristic.

Heuristic: walk candidates nearest first and keep ``e`` only if it is closer
to the new node than to every neighbour already kept. That keeps links
pointing in different directions, which is what makes clustered data
navigable. We follow hnswlib in not back-filling pruned candidates.

Search: greedy descent through the upper layers, then a beam search on
layer 0 with ``ef = max(efSearch, k)``.

Filtering during the descent (ADR-0010): excluded and removed nodes are
still traversed, so they keep the graph connected, but they never enter the
result set. The search stops once the nearest unexplored candidate is
farther than the worst of ``ef`` *allowed* results. A narrow filter
therefore widens the search instead of collapsing recall, as filtering
after the search would.

Distance: ``1 - cosine`` on float32 unit vectors.

Invariants (``check_invariants``): one level per node, every node has a
link list for each of its layers and none above, degree at most ``M`` (or
``M0`` on layer 0), neighbours exist on that layer, no self-links or
duplicates, and the entry point is on the top layer.

Porting notes: link lists are ``list[list[list[int]]]`` (node, layer,
neighbours), i.e. ``Vec<Vec<Vec<u32>>>``; ``heapq`` maps to
``BinaryHeap`` with ``Reverse``. The level generator is seeded, so
construction is deterministic for a given insertion order.
"""

from __future__ import annotations

import heapq
import math

import numpy as np

from ..clustering.common import InvariantViolation
from .common import Hits, normalize, top_k

__all__ = ["HNSWIndex"]


class HNSWIndex:
    """HNSW graph over cosine distance."""

    def __init__(self, dimension: int, m: int = 16, ef_construction: int = 200,
                 ef_search: int = 64, seed: int = 0):
        if m < 2:
            raise ValueError("M must be at least 2")
        self.dimension = dimension
        self.m = m
        self.m0 = 2 * m
        self.ef_construction = max(ef_construction, m)
        self.ef_search = ef_search
        self.level_multiplier = 1.0 / math.log(m)
        self._rng = np.random.default_rng(seed)
        self._vectors = np.zeros((16, dimension), dtype=np.float32)
        self._alive = np.zeros(16, dtype=bool)
        self._levels: list[int] = []
        self._links: list[list[list[int]]] = []
        self.entry_point = -1
        self.top_level = -1
        # Visited marks without clearing an array per search: a node is
        # visited in the current search when its mark equals the epoch.
        self._visit_mark = np.zeros(16, dtype=np.int64)
        self._visit_epoch = 0
        self.distance_computations = 0

    @property
    def count(self) -> int:
        """Nodes stored, including tombstones."""
        return len(self._levels)

    def __len__(self) -> int:
        return int(self._alive[:self.count].sum())

    def add(self, vectors: np.ndarray) -> np.ndarray:
        """Insert rows one by one; returns their ids."""
        rows = normalize(vectors)
        if rows.shape[1] != self.dimension:
            raise ValueError(f"expected dimension {self.dimension}, got {rows.shape[1]}")
        return np.array([self._insert(row) for row in rows], dtype=np.int64)

    def remove(self, ids: np.ndarray) -> None:
        """Tombstone ids: kept for routing, never returned."""
        ids = np.asarray(ids, dtype=np.int64)
        self._alive[ids[(ids >= 0) & (ids < self.count)]] = False

    def search(self, query: np.ndarray, k: int, allowed: np.ndarray | None = None,
               ef_search: int | None = None) -> Hits:
        """Approximate top-k by cosine among live nodes passing ``allowed``."""
        if k <= 0 or self.entry_point < 0:
            return Hits(np.empty(0, np.int64), np.empty(0, np.float32))
        before = self.distance_computations
        unit = normalize(query)[0]
        returnable = self._alive[:self.count].copy()
        if allowed is not None:
            returnable &= np.asarray(allowed[:self.count], dtype=bool)
        if not returnable.any():
            return Hits(np.empty(0, np.int64), np.empty(0, np.float32))
        entry = self._descend(unit, self.entry_point, self.top_level, 0)
        ef = max(ef_search or self.ef_search, k)
        found = self._search_layer(unit, [entry], ef, 0, returnable)
        ids = np.array([node for _, node in found], dtype=np.int64)
        scores = 1.0 - np.array([distance for distance, _ in found], dtype=np.float32)
        ids, scores = top_k(ids, scores, k)
        return Hits(ids, scores, visited=self.distance_computations - before)

    def _insert(self, vector: np.ndarray) -> int:
        node = self.count
        self._grow(node + 1)
        self._vectors[node] = vector
        self._alive[node] = True
        level = int(-math.log(max(self._rng.random(), 1e-300)) * self.level_multiplier)
        self._levels.append(level)
        self._links.append([[] for _ in range(level + 1)])
        if self.entry_point < 0:
            self.entry_point, self.top_level = node, level
            return node

        entry = self._descend(vector, self.entry_point, self.top_level, level + 1)
        entries = [entry]
        for layer in range(min(level, self.top_level), -1, -1):
            candidates = self._search_layer(vector, entries, self.ef_construction, layer, None)
            neighbours = self._select_neighbours(candidates, self.m)
            self._links[node][layer] = neighbours
            limit = self.m0 if layer == 0 else self.m
            for neighbour in neighbours:
                links = self._links[neighbour][layer]
                links.append(node)
                if len(links) > limit:
                    self._links[neighbour][layer] = self._shrink(neighbour, links, limit)
            entries = [n for _, n in candidates]
        if level > self.top_level:
            self.entry_point, self.top_level = node, level
        return node

    def _descend(self, vector: np.ndarray, entry: int, from_level: int, to_level: int) -> int:
        """Greedy walk (ef = 1) from ``from_level`` down to ``to_level`` inclusive."""
        current = entry
        current_distance = self._distances(vector, [current])[0]
        for layer in range(from_level, to_level - 1, -1):
            improved = True
            while improved:
                improved = False
                neighbours = self._links[current][layer]
                if not neighbours:
                    break
                distances = self._distances(vector, neighbours)
                best = int(np.argmin(distances))
                if distances[best] < current_distance:
                    current, current_distance = neighbours[best], distances[best]
                    improved = True
        return current

    def _search_layer(self, vector: np.ndarray, entries: list[int], ef: int, layer: int,
                      returnable: np.ndarray | None) -> list[tuple[float, int]]:
        """Beam search on one layer (Algorithm 2); returns (distance, node), nearest first.

        With ``returnable`` set, every node is explored but only returnable
        ones enter the result set (filtering during the descent).
        """
        self._visit_epoch += 1
        epoch = self._visit_epoch
        entry_distances = self._distances(vector, entries)
        candidates: list[tuple[float, int]] = []
        results: list[tuple[float, int]] = []  # max-heap via negated distance
        for distance, node in zip(entry_distances.tolist(), entries, strict=True):
            if self._visit_mark[node] == epoch:
                continue
            self._visit_mark[node] = epoch
            heapq.heappush(candidates, (distance, node))
            if returnable is None or returnable[node]:
                heapq.heappush(results, (-distance, node))
        while len(results) > ef:
            heapq.heappop(results)
        while candidates:
            distance, node = heapq.heappop(candidates)
            if len(results) >= ef and distance > -results[0][0]:
                break
            fresh = [n for n in self._links[node][layer] if self._visit_mark[n] != epoch]
            if not fresh:
                continue
            self._visit_mark[fresh] = epoch
            for d, neighbour in zip(self._distances(vector, fresh).tolist(), fresh, strict=True):
                if len(results) < ef or d < -results[0][0]:
                    heapq.heappush(candidates, (d, neighbour))
                    if returnable is None or returnable[neighbour]:
                        heapq.heappush(results, (-d, neighbour))
                        if len(results) > ef:
                            heapq.heappop(results)
        return sorted((-negated, node) for negated, node in results)

    def _select_neighbours(self, candidates: list[tuple[float, int]], limit: int) -> list[int]:
        """Algorithm 4: keep a candidate only if it is nearer the base than any kept one."""
        kept: list[int] = []
        for distance, node in candidates:
            if len(kept) >= limit:
                break
            if not kept or np.all(self._distances(self._vectors[node], kept) >= distance):
                kept.append(node)
        return kept

    def _shrink(self, base: int, links: list[int], limit: int) -> list[int]:
        distances = self._distances(self._vectors[base], links)
        ordered = sorted(zip(distances.tolist(), links, strict=True))
        return self._select_neighbours(ordered, limit)

    def _distances(self, vector: np.ndarray, nodes: list[int]) -> np.ndarray:
        self.distance_computations += len(nodes)
        return 1.0 - self._vectors[nodes] @ vector

    def _grow(self, needed: int) -> None:
        if needed <= len(self._vectors):
            return
        capacity = max(needed, 2 * len(self._vectors))
        self._vectors = np.resize(self._vectors, (capacity, self.dimension))
        extra = capacity - len(self._alive)
        self._alive = np.concatenate([self._alive, np.zeros(extra, dtype=bool)])
        self._visit_mark = np.concatenate([self._visit_mark, np.zeros(extra, dtype=np.int64)])

    def layer_sizes(self) -> list[int]:
        """Nodes on each layer, bottom first."""
        return [sum(1 for level in self._levels if level >= layer)
                for layer in range(self.top_level + 1)]

    def check_invariants(self) -> None:
        """Layer, degree and entry-point invariants (see module doc)."""
        n = self.count
        if len(self._links) != n:
            raise InvariantViolation("HNSW has a level without link lists or vice versa")
        if n == 0:
            if self.entry_point != -1:
                raise InvariantViolation("an empty HNSW has an entry point")
            return
        if self.top_level != max(self._levels):
            raise InvariantViolation("HNSW top level is not the highest node level")
        if self._levels[self.entry_point] != self.top_level:
            raise InvariantViolation("HNSW entry point is not on the top layer")
        for node in range(n):
            layers = self._links[node]
            if len(layers) != self._levels[node] + 1:
                raise InvariantViolation(f"node {node} has links for the wrong number of layers")
            for layer, neighbours in enumerate(layers):
                limit = self.m0 if layer == 0 else self.m
                if len(neighbours) > limit:
                    raise InvariantViolation(
                        f"node {node} has {len(neighbours)} links on layer {layer}, max {limit}")
                if len(set(neighbours)) != len(neighbours):
                    raise InvariantViolation(f"node {node} has duplicate links on layer {layer}")
                for neighbour in neighbours:
                    if neighbour == node:
                        raise InvariantViolation(f"node {node} links to itself")
                    if not 0 <= neighbour < n or self._levels[neighbour] < layer:
                        raise InvariantViolation(
                            f"node {node} links to {neighbour}, which is not on layer {layer}")
