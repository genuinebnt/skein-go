"""Turning the model's edit requests into position-based splices.

The model describes an edit as "replace this exact text with that text",
which is robust to the model miscounting positions. Each ``find`` must occur
exactly once in the current text (or the request names which occurrence),
and edits must not overlap, so the result never depends on the order edits
are applied in. Splices come back sorted by descending position: applying
them in that order leaves earlier positions valid.

All positions are in code points.
"""

from __future__ import annotations

import itertools
from dataclasses import dataclass


class EditError(ValueError):
    """An edit request cannot be applied to the current text. The message is
    written for the model, so it can fix the request and retry."""


@dataclass(frozen=True, slots=True)
class EditRequest:
    """Replace ``find`` with ``replace``. ``occurrence`` is 1-based; 0 means
    ``find`` must be unique."""

    find: str
    replace: str
    occurrence: int = 0


@dataclass(frozen=True, slots=True)
class Splice:
    """Delete ``delete_count`` code points at ``index``, then insert ``insert``."""

    index: int
    delete_count: int
    insert: str


def _occurrences(text: str, needle: str) -> list[int]:
    found, start = [], 0
    while (i := text.find(needle, start)) >= 0:
        found.append(i)
        start = i + 1
    return found


def plan_edits(text: str, requests: list[EditRequest]) -> list[Splice]:
    """Resolve requests against ``text``; raise EditError if any is ambiguous,
    missing or overlaps another."""
    if not requests:
        raise EditError("no edits given")
    splices = []
    for n, req in enumerate(requests, 1):
        if not req.find:
            raise EditError(f"edit {n}: 'find' must not be empty; quote the text to replace")
        if req.find == req.replace:
            raise EditError(f"edit {n}: 'find' and 'replace' are identical")
        hits = _occurrences(text, req.find)
        if not hits:
            raise EditError(f"edit {n}: the text to replace was not found in the note")
        if req.occurrence == 0:
            if len(hits) > 1:
                raise EditError(f"edit {n}: the text occurs {len(hits)} times; quote more context or set 'occurrence'")
            index = hits[0]
        elif 1 <= req.occurrence <= len(hits):
            index = hits[req.occurrence - 1]
        else:
            raise EditError(f"edit {n}: occurrence {req.occurrence} requested, text occurs {len(hits)} times")
        splices.append(Splice(index, len(req.find), req.replace))
    splices.sort(key=lambda s: s.index)
    for a, b in itertools.pairwise(splices):
        if a.index + a.delete_count > b.index:
            raise EditError("two edits overlap; combine them into one")
    return sorted(splices, key=lambda s: s.index, reverse=True)


def apply_splices(text: str, splices: list[Splice]) -> str:
    """Apply splices (descending order) to a plain string: the oracle that
    tests compare the CRDT result against."""
    for s in splices:
        text = text[: s.index] + s.insert + text[s.index + s.delete_count :]
    return text
