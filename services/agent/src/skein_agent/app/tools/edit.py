"""``apply_edit``: suggest edits to a note as a CRDT peer (ADR-0009).

The only composite tool, because an edit is not one RPC:

1. ``GetNote`` with Markdown, for the current text and version vector.
2. Register this run's replica with identity, once per run, so documents
   accepts operations from it and blame attributes them to the agent run.
3. Fetch the op log up to that version and replay it into a local
   ``Sequence`` owned by the run's replica.
4. Check the replayed text equals the Markdown GetNote returned. If not
   (for example once block markers exist), refuse rather than guess.
5. Resolve the find/replace requests to splices, generate the deletes and
   inserts locally, and send them in **one** ``ApplyOps`` batch. One batch
   from one replica is one change group, so ``Undo`` reverts the whole
   suggestion and the editor can show it as one suggestion to accept or
   reject.

Contract gap: agent.proto and documents.proto have no explicit
"suggestion" flag. This relies on documents treating change groups from
replicas registered by agent runs as suggestions; rejecting one is
``Undo(note_id, replica_id)``. See the service README.
"""

from __future__ import annotations

from collections.abc import Mapping
from typing import Any

from ... import pb
from ...domain import crdt
from ...domain.edits import EditError, EditRequest, plan_edits
from ...domain.errors import ErrorCode, ServiceError
from ...domain.ids import random_replica_id
from ...domain.policy import Effect
from .base import ArgumentError, Rendered, ToolRunContext
from .schema import proto_comments

_NOTE_ID_DOC = proto_comments().get("skein.v1.GetNoteRequest.note_id", "The note to edit.")


class ApplyEditTool:
    """Suggest find/replace edits to one note as a single change group."""

    name = "apply_edit"
    effect = Effect.WRITE
    description = (
        "Suggest edits to a note. Each edit replaces an exact quoted piece of the note's current "
        "text. All edits in one call become one suggestion the user can accept or reject as a "
        "whole, so put every change to a note in a single call. Read the note with get_note first."
    )

    def schema(self) -> dict[str, Any]:
        """Hand-written: the request is not one proto message."""
        return {
            "type": "object",
            "properties": {
                "note_id": {"type": "string", "description": _NOTE_ID_DOC or "The note to edit."},
                "edits": {
                    "type": "array",
                    "minItems": 1,
                    "items": {
                        "type": "object",
                        "properties": {
                            "find": {"type": "string", "description": "Exact text currently in the note."},
                            "replace": {"type": "string", "description": "Text to put in its place."},
                            "occurrence": {
                                "type": "integer",
                                "minimum": 0,
                                "description": "1-based occurrence if 'find' appears more than once.",
                            },
                        },
                        "required": ["find", "replace"],
                        "additionalProperties": False,
                    },
                },
            },
            "required": ["note_id", "edits"],
            "additionalProperties": False,
        }

    def cache_key(self, args: Mapping[str, Any], ctx: ToolRunContext) -> bytes | None:
        """See Tool.cache_key."""
        return None

    def parse(self, args: Mapping[str, Any]) -> tuple[str, list[EditRequest]]:
        """Validate arguments by hand, since there is no proto to parse into."""
        unknown = set(args) - {"note_id", "edits"}
        if unknown:
            raise ArgumentError(f"unknown arguments: {', '.join(sorted(unknown))}")
        note_id = args.get("note_id")
        edits = args.get("edits")
        if not isinstance(note_id, str) or not note_id:
            raise ArgumentError("note_id must be a non-empty string")
        if not isinstance(edits, list) or not edits:
            raise ArgumentError("edits must be a non-empty list")
        out = []
        for i, e in enumerate(edits, 1):
            if not isinstance(e, Mapping) or set(e) - {"find", "replace", "occurrence"}:
                raise ArgumentError(f"edit {i} must have only find, replace and occurrence")
            find, replace, occ = e.get("find"), e.get("replace"), e.get("occurrence", 0)
            if not isinstance(find, str) or not isinstance(replace, str):
                raise ArgumentError(f"edit {i}: find and replace must be strings")
            if not isinstance(occ, int) or isinstance(occ, bool) or occ < 0:
                raise ArgumentError(f"edit {i}: occurrence must be a non-negative integer")
            out.append(EditRequest(find, replace, occ))
        return note_id, out

    async def run(
        self, args: Mapping[str, Any], ctx: ToolRunContext, cached: bytes | None
    ) -> tuple[Rendered, bytes | None]:
        """See Tool.run."""
        note_id, requests = self.parse(args)
        got = await ctx.call(
            "documents", "GetNote", pb.documents_pb2.GetNoteRequest(note_id=note_id, include_markdown=True)
        )
        if got.note.trashed:
            raise ServiceError(ErrorCode.FAILED_PRECONDITION, "the note is in the trash; restore it first")
        replica = await self._ensure_replica(ctx)
        version = {e.replica_id: e.counter for e in got.version.entries}
        seq = await self._replay(note_id, version, replica, ctx)
        if seq.text() != got.markdown:
            raise ServiceError(
                ErrorCode.FAILED_PRECONDITION,
                "the note's stored text does not match its operation log; edits cannot be placed safely",
            )
        try:
            splices = plan_edits(seq.text(), requests)
        except EditError as err:
            raise ArgumentError(str(err)) from err
        ops: list[crdt.Op] = []
        for s in splices:
            ops.extend(seq.delete(s.index, s.delete_count))
            ops.extend(seq.insert(s.index, s.insert))
        await ctx.call(
            "documents",
            "ApplyOps",
            pb.documents_pb2.ApplyOpsRequest(note_id=note_id, replica_id=replica, ops=crdt.encode_ops(ops)),
        )
        n = len(splices)
        content = (
            f"Suggested {n} edit{'s' if n != 1 else ''} to note_id={note_id} as one change group. "
            "The user sees it as a suggestion and can accept or reject it."
        )
        return Rendered(content, f"Suggested {n} edit{'s' if n != 1 else ''} in 1 note"), None

    async def describe(self, args: Mapping[str, Any], ctx: ToolRunContext) -> str:
        """See Tool.describe."""
        return "Suggest edits to a note"

    async def _ensure_replica(self, ctx: ToolRunContext) -> int:
        if ctx.state.replica_id == 0:
            replica = random_replica_id()
            await ctx.call("identity", "RegisterReplica", pb.identity_pb2.RegisterReplicaRequest(replica_id=replica))
            ctx.state.replica_id = replica
        return ctx.state.replica_id

    async def _replay(self, note_id: str, version: dict[int, int], replica: int, ctx: ToolRunContext) -> crdt.Sequence:
        batches = await ctx.services.op_log.fetch_ops(
            note_id, version, replica_id=replica, token=ctx.token, timeout_s=ctx.timeout()
        )
        seq = crdt.Sequence(replica)
        try:
            for batch in batches:
                for op in crdt.decode_ops(batch):
                    seq.apply(op)
        except crdt.CrdtError as err:
            raise ServiceError(ErrorCode.INTERNAL, f"could not replay the note's operations ({err.kind.name})") from err
        missing = [r for r, c in version.items() if seq.version().get(r, 0) < c]
        if missing:
            raise ServiceError(ErrorCode.UNAVAILABLE, "the operation log was incomplete; try again")
        return seq
