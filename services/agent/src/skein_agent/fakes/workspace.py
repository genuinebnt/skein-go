"""An in-memory workspace standing in for every service the agent calls.

Permission model (ADR-0008, simplified to what the agent exercises):

- Workspace roles owner/admin/member give the editor role on every note in
  the workspace; guests get nothing unless a note grants them a role.
- A per-note role overrides the workspace-derived one.
- READ needs viewer, COMMENT commenter, EDIT and DELETE editor. SHARE needs
  editor on the note and at least member in the workspace. CREATE needs
  member. AGENT_RUN needs any workspace role.
- Tokens: each user has an access token; ``IssueDelegatedToken`` returns a
  token for (user, run) with an expiry. Both are checked on every call;
  delegated tokens get exactly their user's permissions.

Every call is checked against the proto: the RPC must exist on the named
service and the request must be its input type, so a tool wired to the wrong
message fails in tests. Calls honour ``timeout_s`` like a gRPC deadline, and
tests can install a hook per RPC (to block, fail or observe cancellation).

Search, graph and ML behaviour is deliberately naive (word matching, BFS,
bag-of-words cosine, a small knapsack), enough for the agent's control
flow and the evals, not a model of the real services.
"""

from __future__ import annotations

import asyncio
import math
import re
from collections import Counter, deque
from collections.abc import Awaitable, Callable, Iterable
from dataclasses import dataclass, field
from typing import Any

from google.protobuf.message import Message

from .. import pb
from ..app.ports import Services
from ..domain import crdt
from ..domain.errors import ErrorCode, ServiceError
from ..domain.ids import uuid7

d, s, m, i, n = pb.documents_pb2, pb.search_pb2, pb.ml_pb2, pb.identity_pb2, pb.notifications_pb2

SERVER_REPLICA = 1
_WORD = re.compile(r"[\w']+")
_LINK = re.compile(r"\[\[([^\]]+)\]\]")

_SERVICES = {
    "documents": (d, "DocumentService"),
    "search": (s, "SearchService"),
    "graph": (s, "GraphService"),
    "ml": (m, "MLService"),
    "identity": (i, "IdentityService"),
    "notifications": (n, "NotificationService"),
}

Hook = Callable[[Message], Awaitable[None]]


@dataclass
class FakeNote:
    """A note and its CRDT state."""

    id: str
    workspace_id: str
    title: str
    created_by: str
    seq: crdt.Sequence
    tags: tuple[str, ...] = ()
    minutes: int = 0
    trashed: bool = False

    @property
    def text(self) -> str:
        """The note's current visible text."""
        return self.seq.text()


@dataclass(frozen=True)
class Subject:
    """Who a token belongs to."""

    user_id: str
    run_id: str = ""


@dataclass(frozen=True)
class ChangeGroup:
    """One ApplyOps batch: what Undo would revert as a unit."""

    note_id: str
    replica_id: int
    op_count: int
    user_id: str
    run_id: str


@dataclass(frozen=True)
class CallRecord:
    """One RPC the fake served."""

    service: str
    rpc: str
    user_id: str
    run_id: str
    code: str  # "ok" or an ErrorCode value


def _words(text: str) -> list[str]:
    return [w.casefold() for w in _WORD.findall(text)]


def _cosine(a: Counter[str], b: Counter[str]) -> float:
    dot = sum(a[k] * b[k] for k in a.keys() & b.keys())
    na, nb = math.sqrt(sum(v * v for v in a.values())), math.sqrt(sum(v * v for v in b.values()))
    return dot / (na * nb) if na and nb else 0.0


def _levenshtein(a: str, b: str) -> int:
    prev = list(range(len(b) + 1))
    for x, ca in enumerate(a, 1):
        cur = [x]
        for y, cb in enumerate(b, 1):
            cur.append(min(prev[y] + 1, cur[y - 1] + 1, prev[y - 1] + (ca != cb)))
        prev = cur
    return prev[-1]


@dataclass
class FakeWorkspace:
    """The state behind every fake service."""

    clock: Any = None
    users: dict[str, tuple[str, str]] = field(default_factory=dict)  # id -> (email, name)
    user_tokens: dict[str, str] = field(default_factory=dict)
    delegated: dict[str, tuple[Subject, float]] = field(default_factory=dict)
    workspace_roles: dict[tuple[str, str], int] = field(default_factory=dict)
    note_roles: dict[tuple[str, str], int] = field(default_factory=dict)
    notes: dict[str, FakeNote] = field(default_factory=dict)
    replicas: dict[int, Subject] = field(default_factory=dict)
    share_links: dict[str, tuple[str, int]] = field(default_factory=dict)
    inbox: dict[str, list[Message]] = field(default_factory=dict)
    groups: list[ChangeGroup] = field(default_factory=list)
    calls: list[CallRecord] = field(default_factory=list)
    hooks: dict[tuple[str, str], Hook] = field(default_factory=dict)

    def _now(self) -> float:
        import time

        return self.clock.now() if self.clock is not None else time.time()

    # Seeding -------------------------------------------------------------

    def add_user(self, user_id: str, name: str = "", email: str = "") -> str:
        """Add a user and return their access token."""
        self.users[user_id] = (email or f"{user_id}@example.test", name or user_id)
        token = f"user-token-{user_id}"
        self.user_tokens[token] = user_id
        return token

    def add_member(self, workspace_id: str, user_id: str, role: int) -> None:
        """Give a user a workspace role (identity_pb2.WorkspaceRole)."""
        self.workspace_roles[(workspace_id, user_id)] = role

    def set_note_role(self, note_id: str, user_id: str, role: int) -> None:
        """Override a user's role on one note (identity_pb2.NoteRole)."""
        self.note_roles[(note_id, user_id)] = role

    def add_note(
        self,
        workspace_id: str,
        title: str,
        markdown: str,
        *,
        created_by: str = "",
        tags: Iterable[str] = (),
        minutes: int = 0,
        note_id: str = "",
    ) -> str:
        """Create a note directly, as if typed by the server replica."""
        seq = crdt.Sequence(SERVER_REPLICA)
        seq.insert(0, markdown)
        nid = note_id or uuid7()
        self.notes[nid] = FakeNote(nid, workspace_id, title, created_by, seq, tuple(tags), minutes)
        return nid

    def services(self) -> Services:
        """Port implementations backed by this workspace."""
        return Services(
            documents=FakeService(self, "documents"),
            search=FakeService(self, "search"),
            graph=FakeService(self, "graph"),
            ml=FakeService(self, "ml"),
            identity=FakeService(self, "identity"),
            notifications=FakeService(self, "notifications"),
            op_log=FakeOpLog(self),
        )

    def note_by_title(self, title: str) -> FakeNote:
        """Test helper."""
        return next(nt for nt in self.notes.values() if nt.title == title)

    # Authorization -------------------------------------------------------

    def subject_for(self, token: str) -> Subject:
        """Resolve a bearer token or raise UNAUTHENTICATED."""
        if (uid := self.user_tokens.get(token)) is not None:
            return Subject(uid)
        if (entry := self.delegated.get(token)) is not None:
            subject, expires = entry
            if expires > self._now():
                return subject
        raise ServiceError(ErrorCode.UNAUTHENTICATED, "invalid or expired token")

    def note_role(self, user_id: str, note: FakeNote) -> int:
        """Effective role of a user on a note."""
        override = self.note_roles.get((note.id, user_id), 0)
        if override:
            return override
        ws_role = self.workspace_roles.get((note.workspace_id, user_id), 0)
        return i.NOTE_ROLE_EDITOR if ws_role >= i.WORKSPACE_ROLE_MEMBER else 0

    def allowed(self, user_id: str, action: int, resource_id: str) -> bool:
        """The Authorize decision."""
        if action in (
            i.ACTION_NOTE_CREATE,
            i.ACTION_AGENT_RUN,
            i.ACTION_WORKSPACE_MANAGE_MEMBERS,
            i.ACTION_WORKSPACE_MANAGE_SETTINGS,
        ):
            ws_role = self.workspace_roles.get((resource_id, user_id), 0)
            need = {i.ACTION_NOTE_CREATE: i.WORKSPACE_ROLE_MEMBER, i.ACTION_AGENT_RUN: i.WORKSPACE_ROLE_GUEST}
            return ws_role >= need.get(action, i.WORKSPACE_ROLE_ADMIN)
        note = self.notes.get(resource_id)
        if note is None:
            return False
        role = self.note_role(user_id, note)
        if action == i.ACTION_NOTE_READ:
            return role >= i.NOTE_ROLE_VIEWER
        if action == i.ACTION_NOTE_COMMENT:
            return role >= i.NOTE_ROLE_COMMENTER
        if action in (i.ACTION_NOTE_EDIT, i.ACTION_NOTE_DELETE):
            return role >= i.NOTE_ROLE_EDITOR
        if action == i.ACTION_NOTE_SHARE:
            ws_role = self.workspace_roles.get((note.workspace_id, user_id), 0)
            return role >= i.NOTE_ROLE_EDITOR and ws_role >= i.WORKSPACE_ROLE_MEMBER
        return False

    def require(self, subject: Subject, action: int, resource_id: str) -> None:
        """Raise NOT_FOUND for unreadable notes (do not reveal they exist),
        PERMISSION_DENIED for readable ones the subject cannot act on."""
        note = self.notes.get(resource_id)
        if note is not None and not self.allowed(subject.user_id, i.ACTION_NOTE_READ, resource_id):
            raise ServiceError(ErrorCode.NOT_FOUND, f"note {resource_id} not found")
        if note is None and action not in (i.ACTION_NOTE_CREATE, i.ACTION_AGENT_RUN):
            raise ServiceError(ErrorCode.NOT_FOUND, f"note {resource_id} not found")
        if not self.allowed(subject.user_id, action, resource_id):
            name = i.Action.Name(action).removeprefix("ACTION_").lower()
            raise ServiceError(ErrorCode.PERMISSION_DENIED, f"the user lacks permission for {name} on {resource_id}")

    def readable(self, subject: Subject, workspace_id: str, *, include_trashed: bool = False) -> list[FakeNote]:
        """Notes the subject can read in a workspace, in ID order."""
        return [
            nt
            for nid, nt in sorted(self.notes.items())
            if nt.workspace_id == workspace_id
            and (include_trashed or not nt.trashed)
            and self.allowed(subject.user_id, i.ACTION_NOTE_READ, nid)
        ]


class FakeService:
    """One fake gRPC service; handlers are methods named after the RPCs."""

    def __init__(self, ws: FakeWorkspace, name: str) -> None:
        self.ws = ws
        self.name = name
        module, service = _SERVICES[name]
        self._methods = module.DESCRIPTOR.services_by_name[service].methods_by_name
        self._handlers = _HANDLERS[name](ws)

    async def invoke(self, rpc: str, request: Message, *, token: str, timeout_s: float) -> Message:
        """See ServiceClient.invoke."""
        method = self._methods.get(rpc)
        if method is None:
            raise ServiceError(ErrorCode.UNIMPLEMENTED, f"{self.name} has no RPC {rpc}")
        if request.DESCRIPTOR.full_name != method.input_type.full_name:
            raise ServiceError(ErrorCode.INVALID_ARGUMENT, f"{rpc} takes {method.input_type.full_name}")
        subject = Subject("")
        try:
            async with asyncio.timeout(timeout_s):
                subject = self.ws.subject_for(token)
                if (hook := self.ws.hooks.get((self.name, rpc))) is not None:
                    await hook(request)
                handler = getattr(self._handlers, rpc, None)
                if handler is None:
                    raise ServiceError(ErrorCode.UNIMPLEMENTED, f"fake {self.name} does not implement {rpc}")
                response = handler(request, subject)
        except TimeoutError:
            self.ws.calls.append(CallRecord(self.name, rpc, subject.user_id, subject.run_id, "deadline_exceeded"))
            raise ServiceError(ErrorCode.DEADLINE_EXCEEDED, f"{rpc} exceeded its deadline") from None
        except ServiceError as err:
            self.ws.calls.append(CallRecord(self.name, rpc, subject.user_id, subject.run_id, err.code.value))
            raise
        self.ws.calls.append(CallRecord(self.name, rpc, subject.user_id, subject.run_id, "ok"))
        if response.DESCRIPTOR.full_name != method.output_type.full_name:
            raise AssertionError(f"fake {rpc} returned {response.DESCRIPTOR.full_name}")
        return response


class FakeOpLog:
    """OpLogReader over the fake documents' sequences."""

    def __init__(self, ws: FakeWorkspace) -> None:
        self.ws = ws

    async def fetch_ops(
        self, note_id: str, until: dict[int, int], *, replica_id: int, token: str, timeout_s: float
    ) -> list[bytes]:
        """See OpLogReader.fetch_ops."""
        subject = self.ws.subject_for(token)
        owner = self.ws.replicas.get(replica_id)
        if owner is None or owner.user_id != subject.user_id:
            raise ServiceError(ErrorCode.PERMISSION_DENIED, "sync hello names a replica the caller does not own")
        self.ws.require(subject, i.ACTION_NOTE_READ, note_id)
        return [crdt.encode_ops(self.ws.notes[note_id].seq.ops_since({}))]


def _note_proto(nt: FakeNote) -> Message:
    return d.Note(id=nt.id, workspace_id=nt.workspace_id, title=nt.title, created_by=nt.created_by, trashed=nt.trashed)


def _page(items: list[Any], req_page: Message, default: int = 50) -> tuple[list[Any], str]:
    start = int(req_page.page_token or 0)
    size = req_page.page_size or default
    chunk = items[start : start + size]
    nxt = str(start + size) if start + size < len(items) else ""
    return chunk, nxt


class _Documents:
    def __init__(self, ws: FakeWorkspace) -> None:
        self.ws = ws

    def CreateNote(self, req: Message, sub: Subject) -> Message:
        self.ws.require(sub, i.ACTION_NOTE_CREATE, req.workspace_id)
        if not req.title.strip():
            raise ServiceError(ErrorCode.INVALID_ARGUMENT, "title is required")
        nid = self.ws.add_note(req.workspace_id, req.title, req.markdown, created_by=sub.user_id)
        return d.CreateNoteResponse(note=_note_proto(self.ws.notes[nid]))

    def GetNote(self, req: Message, sub: Subject) -> Message:
        self.ws.require(sub, i.ACTION_NOTE_READ, req.note_id)
        nt = self.ws.notes[req.note_id]
        version = d.VersionVector(
            entries=[d.VersionEntry(replica_id=r, counter=c) for r, c in sorted(nt.seq.version().items())]
        )
        return d.GetNoteResponse(
            note=_note_proto(nt), markdown=nt.text if req.include_markdown else "", version=version
        )

    def ListNotes(self, req: Message, sub: Subject) -> Message:
        notes = self.ws.readable(sub, req.workspace_id, include_trashed=req.include_trashed)
        chunk, nxt = _page(notes, req.page)
        return d.ListNotesResponse(
            notes=[_note_proto(nt) for nt in chunk], page=pb.common_pb2.PageResponse(next_page_token=nxt)
        )

    def DeleteNote(self, req: Message, sub: Subject) -> Message:
        self.ws.require(sub, i.ACTION_NOTE_DELETE, req.note_id)
        self.ws.notes[req.note_id].trashed = True
        return d.DeleteNoteResponse()

    def RenameNote(self, req: Message, sub: Subject) -> Message:
        self.ws.require(sub, i.ACTION_NOTE_EDIT, req.note_id)
        if not req.title.strip():
            raise ServiceError(ErrorCode.INVALID_ARGUMENT, "title is required")
        self.ws.notes[req.note_id].title = req.title
        return d.RenameNoteResponse()

    def ApplyOps(self, req: Message, sub: Subject) -> Message:
        self.ws.require(sub, i.ACTION_NOTE_EDIT, req.note_id)
        owner = self.ws.replicas.get(req.replica_id)
        if owner is None or owner.user_id != sub.user_id:
            raise ServiceError(ErrorCode.PERMISSION_DENIED, "replica is not registered to the caller")
        nt = self.ws.notes[req.note_id]
        try:
            ops = crdt.decode_ops(req.ops)
            if any(op.id.replica != req.replica_id for op in ops):
                raise ServiceError(ErrorCode.INVALID_ARGUMENT, "ops from another replica")
            for op in ops:
                nt.seq.apply(op)
        except crdt.CrdtError as err:
            raise ServiceError(ErrorCode.INVALID_ARGUMENT, str(err)) from err
        self.ws.groups.append(ChangeGroup(req.note_id, req.replica_id, len(ops), sub.user_id, sub.run_id))
        version = d.VersionVector(
            entries=[d.VersionEntry(replica_id=r, counter=c) for r, c in sorted(nt.seq.version().items())]
        )
        return d.ApplyOpsResponse(version=version)


def _blocks(nt: FakeNote) -> list[tuple[int, str]]:
    """Paragraphs with their start offsets in code points."""
    out, pos = [], 0
    for para in nt.text.split("\n\n"):
        if para.strip():
            out.append((pos, para))
        pos += len(para) + 2
    return out


class _Search:
    def __init__(self, ws: FakeWorkspace) -> None:
        self.ws = ws

    def Search(self, req: Message, sub: Subject) -> Message:
        hits = []
        terms = _words(req.query)
        for nt in self.ws.readable(sub, req.workspace_id):
            for start, para in _blocks(nt):
                if req.mode == s.SEARCH_MODE_REGEX:
                    try:
                        found = re.search(req.query, para, 0 if req.case_sensitive else re.IGNORECASE)
                    except re.error as err:
                        raise ServiceError(ErrorCode.INVALID_ARGUMENT, f"bad regex: {err}") from err
                    if not found:
                        continue
                    lo, hi, score = found.start(), found.end(), 1.0
                else:
                    words = _words(para)
                    present = [t for t in terms if t in words]
                    if not terms or len(present) < max(1, math.ceil(len(terms) * 0.6)):
                        continue
                    first = para.casefold().find(present[0])
                    lo, hi, score = first, first + len(present[0]), len(present) / len(terms)
                block_id = str(nt.seq.id_at(start))
                hits.append(
                    s.SearchHit(
                        location=pb.common_pb2.BlockRef(note_id=nt.id, block_id=block_id, start=lo, end=hi),
                        note_title=nt.title,
                        snippet=para[:400],
                        highlights=[s.Range(start=lo, end=hi)],
                        score=score,
                    )
                )
        hits.sort(key=lambda h: (-h.score, h.location.note_id, h.location.block_id))
        chunk, nxt = _page(hits, req.page, default=8)
        return s.SearchResponse(
            hits=chunk, page=pb.common_pb2.PageResponse(next_page_token=nxt), candidates=len(hits), matched=len(hits)
        )

    def SuggestTitles(self, req: Message, sub: Subject) -> Message:
        prefix = req.prefix.casefold()
        scored = []
        for nt in self.ws.readable(sub, req.workspace_id):
            title = nt.title.casefold()
            dist = 0 if title.startswith(prefix) or prefix in title else _levenshtein(prefix, title[: len(prefix)])
            if dist <= 2:
                scored.append((dist, nt.title, nt.id))
        scored.sort()
        return s.SuggestTitlesResponse(
            suggestions=[
                s.TitleSuggestion(note_id=nid, title=t, edit_distance=dist) for dist, t, nid in scored[: req.limit or 5]
            ]
        )


class _Graph:
    def __init__(self, ws: FakeWorkspace) -> None:
        self.ws = ws

    def _edges(self, sub: Subject, workspace_id: str) -> dict[str, list[str]]:
        notes = self.ws.readable(sub, workspace_id)
        by_title = {nt.title.casefold(): nt.id for nt in notes}
        return {
            nt.id: sorted(
                {by_title[t.casefold()] for t in _LINK.findall(nt.text) if t.casefold() in by_title} - {nt.id}
            )
            for nt in notes
        }

    def _workspace_of(self, sub: Subject, note_id: str) -> str:
        self.ws.require(sub, i.ACTION_NOTE_READ, note_id)
        return self.ws.notes[note_id].workspace_id

    def GetLinkGraph(self, req: Message, sub: Subject) -> Message:
        edges = self._edges(sub, req.workspace_id)
        return s.GetLinkGraphResponse(
            nodes=[s.GraphNode(note_id=nid, title=self.ws.notes[nid].title) for nid in edges],
            edges=[s.GraphEdge(**{"from": a, "to": b}) for a, outs in edges.items() for b in outs],
        )

    def BlastRadius(self, req: Message, sub: Subject) -> Message:
        edges = self._edges(sub, self._workspace_of(sub, req.note_id))
        hops = {req.note_id: 0}
        queue = deque([req.note_id])
        while queue:
            cur = queue.popleft()
            if req.max_hops and hops[cur] >= req.max_hops:
                continue
            for nxt in edges.get(cur, []):
                if nxt not in hops:
                    hops[nxt] = hops[cur] + 1
                    queue.append(nxt)
        rings = Counter(hops.values())
        return s.BlastRadiusResponse(hops=hops, ring_sizes=[rings[k] for k in range(max(rings) + 1)])

    def ShortestPath(self, req: Message, sub: Subject) -> Message:
        ws_id = self._workspace_of(sub, getattr(req, "from"))
        self._workspace_of(sub, req.to)
        edges = self._edges(sub, ws_id)
        undirected: dict[str, set[str]] = {k: set(v) for k, v in edges.items()}
        for a, outs in edges.items():
            for b in outs:
                undirected.setdefault(b, set()).add(a)
        start, goal = getattr(req, "from"), req.to
        prev: dict[str, str | None] = {start: None}
        queue = deque([start])
        while queue:
            cur = queue.popleft()
            if cur == goal:
                path = []
                node: str | None = cur
                while node is not None:
                    path.append(node)
                    node = prev[node]
                return s.ShortestPathResponse(found=True, path=path[::-1], cost=float(len(path) - 1))
            for nxt in sorted(undirected.get(cur, ())):
                if nxt not in prev:
                    prev[nxt] = cur
                    queue.append(nxt)
        return s.ShortestPathResponse(found=False)

    def ReadingPlan(self, req: Message, sub: Subject) -> Message:
        topic = _words(req.topic)
        items = []
        for nt in self.ws.readable(sub, req.workspace_id):
            words = _words(nt.title + " " + nt.text)
            value = sum(words.count(t) for t in topic)
            if value:
                minutes = nt.minutes or max(1, math.ceil(len(words) / 200))
                items.append((nt.id, minutes, float(value)))
        cap = req.minutes
        best: list[tuple[float, list[int]]] = [(0.0, [])] * (cap + 1)  # 0/1 knapsack over minutes
        for idx, (_, w, v) in enumerate(items):
            for c in range(cap, w - 1, -1):
                cand = best[c - w][0] + v
                if cand > best[c][0]:
                    best[c] = (cand, [*best[c - w][1], idx])
        value, chosen = best[cap]
        planned = [
            s.PlannedNote(
                note_id=items[k][0],
                minutes=items[k][1],
                value=items[k][2],
                reason=f"mentions the topic {int(items[k][2])} times",
            )
            for k in chosen
        ]
        return s.ReadingPlanResponse(notes=planned, total_minutes=sum(p.minutes for p in planned), total_value=value)


class _ML:
    def __init__(self, ws: FakeWorkspace) -> None:
        self.ws = ws

    def SemanticSearch(self, req: Message, sub: Subject) -> Message:
        q = Counter(_words(req.query))
        hits = []
        for nt in self.ws.readable(sub, req.workspace_id):
            if req.tags and not set(req.tags) & set(nt.tags):
                continue
            for start, para in _blocks(nt):
                cos = _cosine(q, Counter(_words(para)))
                if cos > 0:
                    ref = pb.common_pb2.BlockRef(
                        note_id=nt.id, block_id=str(nt.seq.id_at(start)), start=0, end=len(para)
                    )
                    hits.append(m.SemanticHit(location=ref, cosine=cos))
        hits.sort(key=lambda h: (-h.cosine, h.location.note_id))
        return m.SemanticSearchResponse(hits=hits[: req.k or 5])

    def Discover(self, req: Message, sub: Subject) -> Message:
        self.ws.require(sub, i.ACTION_NOTE_READ, req.note_id)
        base = self.ws.notes[req.note_id]
        graph = _Graph(self.ws)
        related = []
        for nt in self.ws.readable(sub, base.workspace_id):
            if nt.id == base.id:
                continue
            cos = _cosine(Counter(_words(base.text)), Counter(_words(nt.text)))
            shared = set(base.tags) & set(nt.tags)
            union = set(base.tags) | set(nt.tags)
            path = graph.ShortestPath(s.ShortestPathRequest(**{"from": base.id, "to": nt.id}), sub)
            hops = len(path.path) - 1 if path.found else -1
            related.append(
                m.RelatedNote(
                    note_id=nt.id,
                    cosine=cos,
                    shared_tags=len(shared),
                    tag_jaccard=len(shared) / len(union) if union else 0.0,
                    hops=hops,
                    suggest_link=cos > 0.3 and hops != 1,
                )
            )
        related.sort(key=lambda r: (-r.cosine, r.note_id))
        return m.DiscoverResponse(related=related[: req.k or 5])

    def ListClusters(self, req: Message, sub: Subject) -> Message:
        groups: dict[str, list[FakeNote]] = {}
        for nt in self.ws.readable(sub, req.workspace_id):
            groups.setdefault(nt.tags[0] if nt.tags else "untagged", []).append(nt)
        clusters = []
        for cid, (label, notes) in enumerate(sorted(groups.items())):
            terms = Counter(w for nt in notes for w in _words(nt.text) if len(w) > 4)
            clusters.append(
                m.Cluster(
                    id=cid,
                    label=label,
                    top_terms=[t for t, _ in terms.most_common(5)],
                    note_ids=[nt.id for nt in notes],
                )
            )
        return m.ListClustersResponse(clusters=clusters)


class _Identity:
    def __init__(self, ws: FakeWorkspace) -> None:
        self.ws = ws

    def IssueDelegatedToken(self, req: Message, sub: Subject) -> Message:
        if sub.run_id or sub.user_id != req.user_id:
            raise ServiceError(ErrorCode.PERMISSION_DENIED, "only the user can delegate their own access")
        token = f"delegated-{req.user_id}-{req.agent_run_id}"
        self.ws.delegated[token] = (Subject(req.user_id, req.agent_run_id), req.expires_at.ToMilliseconds() / 1000)
        return i.IssueDelegatedTokenResponse(access_token=token)

    def Authorize(self, req: Message, sub: Subject) -> Message:
        if req.subject.user_id != sub.user_id:
            raise ServiceError(ErrorCode.PERMISSION_DENIED, "can only authorize as yourself")
        return i.AuthorizeResponse(allowed=[self.ws.allowed(sub.user_id, c.action, c.resource_id) for c in req.checks])

    def RegisterReplica(self, req: Message, sub: Subject) -> Message:
        owner = self.ws.replicas.get(req.replica_id)
        if req.replica_id == 0 or (owner is not None and owner.user_id != sub.user_id):
            raise ServiceError(ErrorCode.FAILED_PRECONDITION, "replica id unavailable")
        self.ws.replicas[req.replica_id] = sub
        return i.RegisterReplicaResponse()

    def SetNoteRole(self, req: Message, sub: Subject) -> Message:
        self.ws.require(sub, i.ACTION_NOTE_SHARE, req.note_id)
        if req.user_id not in self.ws.users:
            raise ServiceError(ErrorCode.NOT_FOUND, f"user {req.user_id} not found")
        if req.role == i.NOTE_ROLE_UNSPECIFIED:
            self.ws.note_roles.pop((req.note_id, req.user_id), None)
        else:
            self.ws.note_roles[(req.note_id, req.user_id)] = req.role
        return i.SetNoteRoleResponse()

    def CreateShareLink(self, req: Message, sub: Subject) -> Message:
        self.ws.require(sub, i.ACTION_NOTE_SHARE, req.note_id)
        if req.role == i.NOTE_ROLE_UNSPECIFIED:
            raise ServiceError(ErrorCode.INVALID_ARGUMENT, "a share link needs a role")
        link_id = uuid7()
        self.ws.share_links[link_id] = (req.note_id, req.role)
        return i.CreateShareLinkResponse(link_id=link_id, token=f"share-secret-{link_id[-8:]}")

    def GetUser(self, req: Message, sub: Subject) -> Message:
        if req.user_id not in self.ws.users:
            raise ServiceError(ErrorCode.NOT_FOUND, "user not found")
        email, name = self.ws.users[req.user_id]
        return i.GetUserResponse(user=i.User(id=req.user_id, email=email, display_name=name))


class _Notifications:
    def __init__(self, ws: FakeWorkspace) -> None:
        self.ws = ws

    def ListInbox(self, req: Message, sub: Subject) -> Message:
        items = [x for x in self.ws.inbox.get(sub.user_id, []) if not (req.unread_only and x.read)]
        return n.ListInboxResponse(notifications=items, unread=sum(1 for x in items if not x.read))


_HANDLERS: dict[str, Callable[[FakeWorkspace], Any]] = {
    "documents": _Documents,
    "search": _Search,
    "graph": _Graph,
    "ml": _ML,
    "identity": _Identity,
    "notifications": _Notifications,
}
