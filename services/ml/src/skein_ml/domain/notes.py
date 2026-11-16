"""The ml service's projection of a note.

`documents` owns notes; ml keeps only what it needs to embed, search and
relate them, rebuilt from ``documents.events``: the workspace, title, the
current text of each block, the outbound links, and whether the note is in
the trash. Tags are derived from the text (`skein_ml.domain.text`).

Blocks are keyed by block id. The events carry no block order, so anything
that needs a canonical order (note text for labels, embeddings) sorts by
block id, which keeps results independent of arrival order.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from .text import extract_tags

__all__ = ["Note"]


@dataclass
class Note:
    """A note as ml sees it."""

    note_id: str
    workspace_id: str = ""
    title: str = ""
    trashed: bool = False
    blocks: dict[str, str] = field(default_factory=dict)
    links: tuple[str, ...] = ()

    @property
    def tags(self) -> frozenset[str]:
        """``#tags`` in the title and every block."""
        found: set[str] = set(extract_tags(self.title))
        for text in self.blocks.values():
            found |= extract_tags(text)
        return frozenset(found)

    def block_ids(self) -> list[str]:
        """Block ids in canonical (sorted) order."""
        return sorted(self.blocks)

    def text(self) -> str:
        """Title and block texts in canonical order, for labelling."""
        return "\n".join([self.title, *(self.blocks[b] for b in self.block_ids())])
