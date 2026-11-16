"""Discovery: related notes with three separate signals, never blended (ADR-0010).

For a target note and each candidate we report

- **meaning**: cosine similarity of the note embeddings;
- **shared tags**: the count and the Jaccard index ``|A & B| / |A | B|``
  (0 when both are empty);
- **link distance**: hops in the link graph ignoring direction, or
  ``NOT_CONNECTED`` (-1) when no path exists.

They are returned side by side; no weighted sum exists anywhere, because a
blend hides why two notes are related and cannot be tuned per user. Results
are ranked by cosine alone (ties by note id), so "related" means "close in
meaning", and the other two columns explain or contradict it.

Where the signals disagree there is a suggestion: ``suggest_link`` is true
when meaning is close (``cosine >= threshold``, 0.70 by default) but the
notes are not already neighbours (``hops >= 2`` or not connected).

Porting notes: BFS over an adjacency built from sorted keys, so results do
not depend on map iteration order.
"""

from __future__ import annotations

from collections import deque
from collections.abc import Iterable, Mapping
from dataclasses import dataclass

import numpy as np

__all__ = [
    "DEFAULT_SUGGEST_THRESHOLD",
    "NOT_CONNECTED",
    "RelatedNote",
    "jaccard",
    "link_hops",
    "related_notes",
    "should_suggest_link",
]

NOT_CONNECTED = -1
"""Hop count for notes with no path between them."""
DEFAULT_SUGGEST_THRESHOLD = 0.70
"""Cosine at or above which unlinked notes are suggested as links."""


@dataclass(frozen=True)
class RelatedNote:
    """The three discovery signals for one candidate, plus the derived suggestion."""

    note_id: str
    cosine: float
    shared_tags: int
    tag_jaccard: float
    hops: int
    suggest_link: bool


def jaccard(a: frozenset[str], b: frozenset[str]) -> float:
    """``|a & b| / |a | b|``, and 0.0 when both are empty."""
    union = len(a | b)
    return len(a & b) / union if union else 0.0


def should_suggest_link(cosine: float, hops: int,
                        threshold: float = DEFAULT_SUGGEST_THRESHOLD) -> bool:
    """Close in meaning but not directly linked (or not connected at all)."""
    return cosine >= threshold and (hops >= 2 or hops == NOT_CONNECTED)


def link_hops(outbound: Mapping[str, Iterable[str]], source: str) -> dict[str, int]:
    """Undirected BFS distances from ``source``; unreachable notes are absent."""
    adjacency: dict[str, set[str]] = {}
    for note in sorted(outbound):
        for target in outbound[note]:
            if target == note:
                continue
            adjacency.setdefault(note, set()).add(target)
            adjacency.setdefault(target, set()).add(note)
    distances = {source: 0}
    queue = deque([source])
    while queue:
        node = queue.popleft()
        for neighbour in sorted(adjacency.get(node, ())):
            if neighbour not in distances:
                distances[neighbour] = distances[node] + 1
                queue.append(neighbour)
    return distances


def related_notes(target: str, embeddings: Mapping[str, np.ndarray],
                  tags: Mapping[str, frozenset[str]], outbound: Mapping[str, Iterable[str]],
                  k: int, threshold: float = DEFAULT_SUGGEST_THRESHOLD) -> list[RelatedNote]:
    """The ``k`` notes closest in meaning to ``target``, each with all three signals.

    ``embeddings`` must hold unit vectors from one embedding space and
    include ``target``; candidates are the other keys of ``embeddings``.
    """
    if target not in embeddings:
        raise KeyError(target)
    candidates = sorted(note for note in embeddings if note != target)
    if not candidates or k <= 0:
        return []
    matrix = np.stack([embeddings[note] for note in candidates]).astype(np.float32)
    cosines = matrix @ np.asarray(embeddings[target], dtype=np.float32)
    order = sorted(range(len(candidates)), key=lambda i: (-float(cosines[i]), candidates[i]))[:k]
    hops = link_hops(outbound, target)
    own_tags = tags.get(target, frozenset())
    related = []
    for i in order:
        note = candidates[i]
        other_tags = tags.get(note, frozenset())
        distance = hops.get(note, NOT_CONNECTED)
        cosine = float(cosines[i])
        related.append(RelatedNote(
            note_id=note, cosine=cosine, shared_tags=len(own_tags & other_tags),
            tag_jaccard=jaccard(own_tags, other_tags), hops=distance,
            suggest_link=should_suggest_link(cosine, distance, threshold)))
    return related
