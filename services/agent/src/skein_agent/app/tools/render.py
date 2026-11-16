"""Turning responses into what the model reads and what the user sees.

Rules every renderer follows:

- Anything that came from the workspace (text, titles, snippets, labels) is
  inside an untrusted block (``domain/untrusted.py``). Read tools wrap their
  whole result; nothing written by other people reaches the model bare.
- Locations the model may cite are registered in the run's SourceRegistry
  and labelled on the block (``source="S3"``).
- Summaries are short, generic and never quote content: they are shown in
  the UI next to the tool call and recorded in traces and spans.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from typing import Any

from google.protobuf import json_format
from google.protobuf.message import Message

from ... import pb
from ...domain.errors import ServiceError
from ...domain.untrusted import wrap_untrusted
from .base import Rendered, ToolRunContext


def _plural(n: int, word: str, plural: str | None = None) -> str:
    return f"{n} {word if n == 1 else (plural or word + 's')}"


def _truncate(text: str, limit: int) -> str:
    if len(text) <= limit:
        return text
    return text[:limit] + f"\n[truncated: {len(text) - limit} more characters]"


def to_json(message: Message) -> str:
    """Compact JSON with proto field names, deterministic key order."""
    return json.dumps(
        json_format.MessageToDict(message, preserving_proto_field_name=True), sort_keys=True, ensure_ascii=False
    )


def wrapped_json(message: Message, ctx: ToolRunContext, summary: str) -> Rendered:
    """Generic renderer for read tools: the whole response as one untrusted block."""
    body = _truncate(to_json(message), ctx.max_result_chars)
    return Rendered(wrap_untrusted(body, nonce=ctx.nonce, attributes={"kind": "tool_result"}), summary)


def plain(text: str, summary: str) -> Rendered:
    """For write tools whose response holds no workspace content."""
    return Rendered(text, summary)


def render_search(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Each hit becomes a citable source with its snippet."""
    hits = list(resp.hits)
    blocks = []
    budget = ctx.max_result_chars
    for hit in hits:
        loc = hit.location
        source = ctx.sources.add(loc.note_id, loc.block_id, loc.start, loc.end, hit.snippet)
        body = f"title: {hit.note_title}\n{_truncate(hit.snippet, budget)}"
        budget = max(200, budget - len(body))
        attrs = {"source": source.label, "note_id": loc.note_id, "block_id": loc.block_id}
        if hit.language:
            attrs["language"] = hit.language
        blocks.append(wrap_untrusted(body, nonce=ctx.nonce, attributes=attrs))
    header = f"{_plural(len(hits), 'result')}."
    if resp.page.next_page_token:
        header += f" More available with page_token={resp.page.next_page_token!r}."
    return Rendered("\n".join([header, *blocks]), f"Found {_plural(len(hits), 'match', 'matches')}")


def render_get_note(resp: Message, ctx: ToolRunContext) -> Rendered:
    """The note's text is one citable source."""
    note = resp.note
    source = ctx.sources.add(note.id, "", 0, 0, resp.markdown[:280])
    meta = f"note_id: {note.id}\ntitle: {note.title}\ntrashed: {str(note.trashed).lower()}\n\n"
    body = meta + _truncate(resp.markdown, ctx.max_result_chars)
    content = wrap_untrusted(body, nonce=ctx.nonce, attributes={"source": source.label, "note_id": note.id})
    return Rendered(content, "Read 1 note")


def render_list_notes(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Titles are content too, so the list is wrapped."""
    return wrapped_json(resp, ctx, f"Listed {_plural(len(resp.notes), 'note')}")


def render_suggest_titles(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Render a SuggestTitles response."""
    return wrapped_json(resp, ctx, f"Found {_plural(len(resp.suggestions), 'title')}")


def render_semantic_search(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Hits are locations without text; each is citable, and the model can
    read the note for the words."""
    lines = []
    for hit in resp.hits:
        loc = hit.location
        source = ctx.sources.add(loc.note_id, loc.block_id, loc.start, loc.end, "")
        lines.append(f"{source.label}: note_id={loc.note_id} block_id={loc.block_id} cosine={hit.cosine:.3f}")
    body = "\n".join(lines) or "no matches"
    return Rendered(
        wrap_untrusted(body, nonce=ctx.nonce, attributes={"kind": "semantic_hits"}),
        f"Found {_plural(len(resp.hits), 'semantic match', 'semantic matches')}",
    )


def render_blast_radius(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Render a BlastRadius response."""
    reached = max(0, len(resp.hops) - 1)
    rings = max(0, len(resp.ring_sizes) - 1)
    return wrapped_json(resp, ctx, f"Reached {_plural(reached, 'note')} within {_plural(rings, 'hop')}")


def render_shortest_path(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Render a ShortestPath response."""
    summary = f"Found a path of {_plural(len(resp.path), 'note')}" if resp.found else "No path found"
    return wrapped_json(resp, ctx, summary)


def render_reading_plan(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Render a ReadingPlan response."""
    return wrapped_json(resp, ctx, f"Planned {_plural(len(resp.notes), 'note')} ({resp.total_minutes} min)")


def render_discover(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Render a Discover response."""
    return wrapped_json(resp, ctx, f"Found {_plural(len(resp.related), 'related note')}")


def render_list_clusters(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Render a ListClusters response."""
    return wrapped_json(resp, ctx, f"Listed {_plural(len(resp.clusters), 'cluster')}")


def render_list_inbox(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Render a ListInbox response."""
    return wrapped_json(resp, ctx, f"Read {_plural(len(resp.notifications), 'notification')}")


def render_create_note(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Render a CreateNote response."""
    return plain(f"Created note_id={resp.note.id}.", "Created 1 note")


def render_rename_note(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Render a RenameNote response."""
    return plain("Renamed the note.", "Renamed 1 note")


def render_delete_note(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Render a DeleteNote response."""
    return plain("Moved the note to the trash. It can be restored.", "Moved 1 note to the trash")


def render_set_note_role(resp: Message, ctx: ToolRunContext) -> Rendered:
    """Render a SetNoteRole response."""
    return plain("Changed the note role.", "Changed 1 note role")


def render_create_share_link(resp: Message, ctx: ToolRunContext) -> Rendered:
    """The token is shown once; the model passes it to the user who asked for
    it. The stored trace keeps only the link ID."""
    content = f"Created share link link_id={resp.link_id} token={resp.token}. Give the token to the user."
    return Rendered(
        content, "Created 1 share link", for_trace=f"Created share link link_id={resp.link_id} token=[redacted]"
    )


async def note_title(note_id: str, ctx: ToolRunContext) -> str:
    """A note's title for confirmation text, or its ID if it cannot be read."""
    try:
        resp = await ctx.call("documents", "GetNote", pb.documents_pb2.GetNoteRequest(note_id=note_id))
        return f'"{resp.note.title}"' if resp.note.title else note_id
    except ServiceError:
        return f"note {note_id}"


async def user_label(user_id: str, ctx: ToolRunContext) -> str:
    """A user's display name for confirmation text, or their ID."""
    try:
        resp = await ctx.call("identity", "GetUser", pb.identity_pb2.GetUserRequest(user_id=user_id))
        name = resp.user.display_name or user_id
        return f"{name} <{resp.user.email}>" if resp.user.email else name
    except ServiceError:
        return f"user {user_id}"


def _role_name(value: Any) -> str:
    if isinstance(value, int):
        value = pb.identity_pb2.NoteRole.Name(value)
    return str(value or "NOTE_ROLE_UNSPECIFIED").removeprefix("NOTE_ROLE_").lower()


async def describe_delete_note(args: Mapping[str, Any], ctx: ToolRunContext) -> str:
    """Confirmation text for delete_note."""
    title = await note_title(str(args.get("note_id", "")), ctx)
    return f"Move {title} to the trash. It can be restored from the trash until purged."


async def describe_set_note_role(args: Mapping[str, Any], ctx: ToolRunContext) -> str:
    """Confirmation text for set_note_role."""
    title = await note_title(str(args.get("note_id", "")), ctx)
    who = await user_label(str(args.get("user_id", "")), ctx)
    role = _role_name(args.get("role"))
    if role == "unspecified":
        return f"Remove the note-specific role of {who} on {title}; they fall back to their workspace role."
    return f"Give {who} the {role} role on {title}."


async def describe_create_share_link(args: Mapping[str, Any], ctx: ToolRunContext) -> str:
    """Confirmation text for create_share_link."""
    title = await note_title(str(args.get("note_id", "")), ctx)
    role = _role_name(args.get("role"))
    expiry = args.get("expires_at")
    until = f", expiring {expiry}" if expiry else ", with no expiry"
    return f"Create a link that gives anyone who has it the {role} role on {title}{until}."
