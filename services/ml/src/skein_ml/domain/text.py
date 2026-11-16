"""Tokenising, tag extraction and cluster labelling for note text.

Tags are ``#hashtags`` in block text (``#rust``, ``#db/infra``), lower-cased.
The events carry no separate tag list, so the ml service derives tags from
the text it already receives in ``BlocksChanged``; a ``#`` directly after a
word character or another ``#`` (``C#``, ``##``, URL fragments) is not a tag.

Cluster labels use class-based TF-IDF (as in BERTopic): a term's weight in
cluster ``c`` is ``tf(t, c) / |c| * log(1 + A / f(t))``, where ``A`` is the
mean number of terms per cluster and ``f(t)`` the term's frequency over all
clusters. Terms frequent in one cluster but rare overall rank first.

Porting notes: ``\\w`` is Unicode-aware in Python's ``re``; Rust's ``regex``
matches that by default. Sorting is by (weight desc, term asc), never by
dict order.
"""

from __future__ import annotations

import math
import re
from collections import Counter
from collections.abc import Iterable, Sequence

__all__ = ["STOP_WORDS", "extract_tags", "tokens", "top_terms"]

_WORD = re.compile(r"\w+", re.UNICODE)
_TAG = re.compile(r"(?<![\w#])#([^\W\d_][\w/-]*)", re.UNICODE)

STOP_WORDS = frozenset(
    """a about above after again against all am an and any are as at be because been before
    being below between both but by can did do does doing down during each few for from further
    had has have having he her here hers herself him himself his how i if in into is it its
    itself just me more most my myself no nor not now of off on once only or other our ours
    ourselves out over own same she should so some such than that the their theirs them
    themselves then there these they this those through to too under until up very was we
    were what when where which while who whom why will with you your yours yourself yourselves
    """.split()
)
"""English function words left out of cluster labels (not out of embeddings)."""


def tokens(text: str) -> list[str]:
    """Lower-cased word tokens."""
    return [match.group(0).lower() for match in _WORD.finditer(text)]


def extract_tags(text: str) -> frozenset[str]:
    """The ``#tags`` mentioned in ``text``, lower-cased, without the ``#``."""
    return frozenset(match.group(1).lower().rstrip("/-") for match in _TAG.finditer(text))


def top_terms(clusters: Sequence[Iterable[str]], count: int = 5) -> list[list[str]]:
    """The ``count`` most distinctive terms of each cluster's concatenated texts."""
    term_counts = []
    for texts in clusters:
        counter: Counter[str] = Counter()
        for text in texts:
            counter.update(
                t for t in tokens(text) if t not in STOP_WORDS and len(t) > 1 and not t.isdigit()
            )
        term_counts.append(counter)
    overall: Counter[str] = Counter()
    for counter in term_counts:
        overall.update(counter)
    sizes = [sum(counter.values()) for counter in term_counts]
    average = sum(sizes) / max(1, len(sizes))
    labels = []
    for counter, size in zip(term_counts, sizes, strict=True):
        weighted = (
            [
                (count_t / size * math.log(1 + average / overall[term]), term)
                for term, count_t in counter.items()
            ]
            if size
            else []
        )
        weighted.sort(key=lambda item: (-item[0], item[1]))
        labels.append([term for _, term in weighted[:count]])
    return labels
