"""A Python port of Skein's sequence CRDT, version 1 (docs/spec/crdt-sequence.md).

Why the agent needs it: ADR-0009 says the agent edits as a CRDT peer with its
own replica ID, so its edits show as its own in history and undo as one
group. ``DocumentService.ApplyOps`` takes encoded operations, and operations
name element IDs, not positions; to turn "replace this text" into
operations the agent must hold a replica of the note. It fetches the op log,
applies it here, generates inserts and deletes with its own replica ID, and
sends those.

This is a line-for-line port of the reference implementation in
``libs/crdt`` (Fugue; see that package's documentation for the algorithm),
kept deliberately simple: a tree that defines the order, plus a doubly
linked list of every node in traversal order. It passes the shared
conformance fixtures in ``testdata/conformance/crdt`` and the golden bytes
of the codec, which is what keeps the two languages in agreement.

Positions count Unicode code points, which is what Python ``str`` indexes.

Invariants I1 to I7 of the spec are checked by ``Sequence.check_invariants``;
tests call it after every operation.
"""

from __future__ import annotations

import enum
from dataclasses import dataclass
from typing import NamedTuple


class CrdtErrorKind(enum.Enum):
    """Sentinel error kinds, matching the Go package's errors."""

    INVALID_OP = "invalid operation"
    NOT_READY = "operation is not causally ready"
    ID_CONFLICT = "operation id reused with different content"
    INDEX_OUT_OF_RANGE = "index out of range"
    INVALID_REPLICA = "replica id 0 is reserved"
    CORRUPT = "corrupt encoding"
    INVARIANT = "invariant violated"


class CrdtError(Exception):
    """A CRDT failure; ``kind`` says which."""

    def __init__(self, kind: CrdtErrorKind, detail: str = "") -> None:
        super().__init__(f"crdt: {kind.value}" + (f": {detail}" if detail else ""))
        self.kind = kind


class ID(NamedTuple):
    """Operation and element ID. Tuple order is the spec's order: replica,
    then counter."""

    replica: int
    counter: int

    def __str__(self) -> str:
        return f"{self.replica}:{self.counter}"


ROOT = ID(0, 0)
_U64 = (1 << 64) - 1


class OpKind(enum.IntEnum):
    """Wire values of operation kinds."""

    INSERT = 1
    DELETE = 2


class Side(enum.IntEnum):
    """Wire values of sides."""

    LEFT = 0
    RIGHT = 1


@dataclass(frozen=True, slots=True)
class Op:
    """One operation. Fields not used by ``kind`` must be zero, so equal ops
    compare and encode identically."""

    id: ID
    lamport: int
    kind: OpKind
    parent: ID = ROOT
    side: Side = Side.LEFT
    value: int = 0
    target: ID = ROOT

    def validate(self) -> None:
        """Raise CrdtError(INVALID_OP) for anything a correct replica cannot produce."""

        def bad(why: str) -> CrdtError:
            return CrdtError(CrdtErrorKind.INVALID_OP, why)

        for n in (*self.id, *self.parent, *self.target, self.lamport):
            if not 0 <= n <= _U64:
                raise bad("integer outside u64")
        if self.id.replica == 0:
            raise bad("replica 0 is reserved for the root")
        if self.id.counter == 0:
            raise bad("counters start at 1")
        if self.kind is OpKind.INSERT:
            if self.side not in (Side.LEFT, Side.RIGHT):
                raise bad(f"unknown side {self.side}")
            if not (0 <= self.value <= 0x10FFFF) or 0xD800 <= self.value <= 0xDFFF:
                raise bad(f"value {self.value:#x} is not a Unicode scalar value")
            if self.parent == self.id:
                raise bad("an element cannot be its own parent")
            if self.parent == ROOT and self.side is Side.LEFT:
                raise bad("the root has no left children")
            if self.parent.replica == self.id.replica and self.parent.counter > self.id.counter:
                raise bad(f"parent {self.parent} is newer than the op that uses it")
            if self.target != ROOT:
                raise bad("insert must not set target")
        elif self.kind is OpKind.DELETE:
            if self.target == ROOT:
                raise bad("the root cannot be deleted")
            if self.target.counter == 0:
                raise bad("delete target has counter 0")
            if self.target.replica == self.id.replica and self.target.counter >= self.id.counter:
                raise bad(f"target {self.target} is not older than the op that deletes it")
            if self.parent != ROOT or self.side != Side.LEFT or self.value != 0:
                raise bad("delete must not set insert fields")
        else:
            raise bad(f"unknown kind {self.kind}")


class _Node:
    __slots__ = ("deleted", "id", "left", "next", "parent", "prev", "right", "side", "value")

    def __init__(self, id: ID, value: int = 0, parent: _Node | None = None, side: Side = Side.LEFT) -> None:
        self.id = id
        self.value = value
        self.deleted = False
        self.parent = parent
        self.side = side
        self.left: list[_Node] = []
        self.right: list[_Node] = []
        self.prev: _Node | None = None
        self.next: _Node | None = None


class Sequence:
    """One replica's copy of a replicated text. Not safe for concurrent use."""

    def __init__(self, replica: int) -> None:
        if not 0 < replica <= _U64:
            raise CrdtError(CrdtErrorKind.INVALID_REPLICA)
        self.replica = replica
        self.lamport = 0
        self._root = _Node(ROOT)
        self._nodes: dict[ID, _Node] = {ROOT: self._root}
        self._vv: dict[int, int] = {}
        self._log: list[Op] = []
        self._log_pos: dict[ID, int] = {}
        self._visible = 0

    def __len__(self) -> int:
        return self._visible

    def version(self) -> dict[int, int]:
        """A copy of the version vector."""
        return dict(self._vv)

    def text(self) -> str:
        """The visible text."""
        out = []
        n = self._root.next
        while n is not None:
            if not n.deleted:
                out.append(chr(n.value))
            n = n.next
        return "".join(out)

    def id_at(self, index: int) -> ID:
        """The element ID at a visible index."""
        if not 0 <= index < self._visible:
            raise CrdtError(CrdtErrorKind.INDEX_OUT_OF_RANGE, f"index {index} in length {self._visible}")
        return self._visible_at(index).id

    def insert(self, index: int, text: str) -> list[Op]:
        """Insert ``text`` so its first code point lands at ``index``; return
        the (already applied) ops."""
        if not 0 <= index <= self._visible:
            raise CrdtError(CrdtErrorKind.INDEX_OUT_OF_RANGE, f"insert at {index} in length {self._visible}")
        left = self._root if index == 0 else self._visible_at(index - 1)
        ops = []
        for ch in text:
            op = self._new_insert(left, ord(ch))
            self._apply_local(op)
            ops.append(op)
            left = self._nodes[op.id]
        return ops

    def delete(self, index: int, count: int) -> list[Op]:
        """Delete ``count`` visible code points from ``index``; return the ops."""
        if index < 0 or count < 0 or index + count > self._visible:
            raise CrdtError(CrdtErrorKind.INDEX_OUT_OF_RANGE, f"delete {count} at {index} in length {self._visible}")
        if count == 0:
            return []
        # Collect targets first: each delete shifts what sits at a visible index.
        targets = []
        n: _Node | None = self._visible_at(index)
        while n is not None and len(targets) < count:
            if not n.deleted:
                targets.append(n)
            n = n.next
        ops = []
        for t in targets:
            op = Op(self._next_id(), self.lamport + 1, OpKind.DELETE, target=t.id)
            self._apply_local(op)
            ops.append(op)
        return ops

    def apply(self, op: Op) -> None:
        """Apply an op from any replica. Re-applying an identical op is a no-op."""
        op.validate()
        if op.id.counter <= self._vv.get(op.id.replica, 0):
            pos = self._log_pos.get(op.id)
            if pos is not None and self._log[pos] == op:
                return
            raise CrdtError(CrdtErrorKind.ID_CONFLICT, str(op.id))
        want = self._vv.get(op.id.replica, 0) + 1
        if op.id.counter != want:
            raise CrdtError(CrdtErrorKind.NOT_READY, f"{op.id} arrived before {op.id.replica}:{want}")
        if op.kind is OpKind.INSERT:
            self._apply_insert(op)
        else:
            self._apply_delete(op)

    def ops_since(self, version: dict[int, int]) -> list[Op]:
        """Every applied op a replica at ``version`` has not seen, in causal order."""
        return [op for op in self._log if op.id.counter > version.get(op.id.replica, 0)]

    def _apply_local(self, op: Op) -> None:
        try:
            self.apply(op)
        except CrdtError as err:  # a locally generated op is always valid and ready
            raise CrdtError(CrdtErrorKind.INVARIANT, f"local op rejected: {err}") from err

    def _next_id(self) -> ID:
        return ID(self.replica, self._vv.get(self.replica, 0) + 1)

    def _new_insert(self, left: _Node, value: int) -> Op:
        if not left.right:
            return Op(self._next_id(), self.lamport + 1, OpKind.INSERT, left.id, Side.RIGHT, value)
        # left.next starts left's first right subtree and has no left children.
        assert left.next is not None
        return Op(self._next_id(), self.lamport + 1, OpKind.INSERT, left.next.id, Side.LEFT, value)

    def _apply_insert(self, op: Op) -> None:
        parent = self._nodes.get(op.parent)
        if parent is None:
            raise CrdtError(CrdtErrorKind.NOT_READY, f"parent {op.parent} of {op.id} not applied")
        n = _Node(op.id, op.value, parent, op.side)
        if op.side is Side.RIGHT:
            i = _insert_sorted(parent.right, n)
            after = parent if i == 0 else _last_in_subtree(parent.right[i - 1])
            _link_after(after, n)
        else:
            i = _insert_sorted(parent.left, n)
            before = parent if i + 1 >= len(parent.left) else _first_in_subtree(parent.left[i + 1])
            assert before.prev is not None  # the root is first and has no left children
            _link_after(before.prev, n)
        self._nodes[n.id] = n
        self._visible += 1
        self._record(op)

    def _apply_delete(self, op: Op) -> None:
        target = self._nodes.get(op.target)
        if target is None:
            raise CrdtError(CrdtErrorKind.NOT_READY, f"target {op.target} of {op.id} not applied")
        if not target.deleted:
            target.deleted = True
            self._visible -= 1
        self._record(op)

    def _record(self, op: Op) -> None:
        self._vv[op.id.replica] = op.id.counter
        self.lamport = max(self.lamport, op.lamport)
        self._log_pos[op.id] = len(self._log)
        self._log.append(op)

    def _visible_at(self, i: int) -> _Node:
        n = self._root.next
        while n is not None:
            if not n.deleted:
                if i == 0:
                    return n
                i -= 1
            n = n.next
        raise CrdtError(CrdtErrorKind.INDEX_OUT_OF_RANGE, "visible index past the end")

    def check_invariants(self) -> None:
        """Raise CrdtError(INVARIANT) for the first of I1..I7 that fails."""

        def fail(msg: str) -> CrdtError:
            return CrdtError(CrdtErrorKind.INVARIANT, msg)

        root = self._root
        if root.id != ROOT or root.parent is not None or root.prev is not None or root.left:
            raise fail("I1: root has an id, parent, predecessor or left children")
        if self._nodes.get(ROOT) is not root:
            raise fail("I1: root missing from the node index")

        order: list[_Node] = []
        stack: list[tuple[_Node, bool]] = [(root, False)]
        while stack:  # iterative in-order walk; deep trees would overflow recursion
            n, expanded = stack.pop()
            if expanded:
                order.append(n)
                continue
            for side, children in ((Side.LEFT, n.left), (Side.RIGHT, n.right)):
                for i, c in enumerate(children):
                    if c.parent is not n or c.side != side:
                        raise fail(f"I2: node {c.id} has wrong parent or side")
                    if i > 0 and not children[i - 1].id < c.id:
                        raise fail(f"I3: children of {n.id} not strictly ascending at {c.id}")
            for c in reversed(n.right):
                stack.append((c, False))
            stack.append((n, True))
            for c in reversed(n.left):
                stack.append((c, False))
        if len(order) != len(self._nodes):
            raise fail(f"I4: tree has {len(order)} nodes, index has {len(self._nodes)}")
        listed: _Node | None = root
        prev: _Node | None = None
        for n in order:
            if listed is not n or n.prev is not prev:
                raise fail(f"I4: linked list diverges from traversal at {n.id}")
            prev, listed = n, n.next
        if listed is not None:
            raise fail("I4: linked list continues past the traversal")
        visible = sum(1 for n in order if n is not root and not n.deleted)
        if visible != self._visible:
            raise fail(f"I5: visible length {self._visible}, counted {visible}")
        expected_next: dict[int, int] = {}
        for op in self._log:
            want = expected_next.get(op.id.replica, 0) + 1
            if op.id.counter != want:
                raise fail(f"I6: log has {op.id} where counter {want} was expected")
            expected_next[op.id.replica] = op.id.counter
            if op.kind is OpKind.INSERT and op.id not in self._nodes:
                raise fail(f"I7: insert {op.id} has no node")
            if op.lamport > self.lamport:
                raise fail(f"I7: lamport clock {self.lamport} below logged {op.lamport}")
        if expected_next != self._vv:
            raise fail("I6: version vector does not match the log")


def _insert_sorted(siblings: list[_Node], n: _Node) -> int:
    i = len(siblings)
    while i > 0 and n.id < siblings[i - 1].id:
        i -= 1
    siblings.insert(i, n)
    return i


def _first_in_subtree(n: _Node) -> _Node:
    while n.left:
        n = n.left[0]
    return n


def _last_in_subtree(n: _Node) -> _Node:
    while n.right:
        n = n.right[-1]
    return n


def _link_after(at: _Node, n: _Node) -> None:
    n.prev, n.next = at, at.next
    if at.next is not None:
        at.next.prev = n
    at.next = n


# Wire format, version 1 (spec section 7).
_MAGIC = b"SK"
_VERSION = 1
_MIN_ENCODED_OP = 6


def _put_uvarint(buf: bytearray, v: int) -> None:
    while v >= 0x80:
        buf.append((v & 0x7F) | 0x80)
        v >>= 7
    buf.append(v)


def encode_ops(ops: list[Op]) -> bytes:
    """Encode a batch exactly as the Go codec does."""
    buf = bytearray(_MAGIC)
    buf.append(_VERSION)
    _put_uvarint(buf, len(ops))
    for op in ops:
        buf.append(int(op.kind))
        for v in (op.id.replica, op.id.counter, op.lamport):
            _put_uvarint(buf, v)
        if op.kind is OpKind.INSERT:
            _put_uvarint(buf, op.parent.replica)
            _put_uvarint(buf, op.parent.counter)
            buf.append(int(op.side))
            _put_uvarint(buf, op.value)
        else:
            _put_uvarint(buf, op.target.replica)
            _put_uvarint(buf, op.target.counter)
    return bytes(buf)


class _Reader:
    def __init__(self, data: bytes, pos: int) -> None:
        self.data, self.pos = data, pos

    def byte(self) -> int:
        if self.pos >= len(self.data):
            raise CrdtError(CrdtErrorKind.CORRUPT, f"unexpected end at byte {self.pos}")
        b = self.data[self.pos]
        self.pos += 1
        return b

    def uvarint(self) -> int:
        start, value, shift = self.pos, 0, 0
        while True:
            if self.pos >= len(self.data):
                raise CrdtError(CrdtErrorKind.CORRUPT, f"unexpected end at byte {start}")
            if self.pos - start >= 10:
                raise CrdtError(CrdtErrorKind.CORRUPT, f"varint overflow at byte {start}")
            b = self.data[self.pos]
            self.pos += 1
            value |= (b & 0x7F) << shift
            if b < 0x80:
                break
            shift += 7
        if value > _U64:
            raise CrdtError(CrdtErrorKind.CORRUPT, f"varint overflow at byte {start}")
        minimal = bytearray()
        _put_uvarint(minimal, value)
        if len(minimal) != self.pos - start:
            raise CrdtError(CrdtErrorKind.CORRUPT, f"non-minimal varint at byte {start}")
        return value

    def id(self) -> ID:
        return ID(self.uvarint(), self.uvarint())


def decode_ops(data: bytes) -> list[Op]:
    """Decode a batch, rejecting everything the spec says to reject."""
    if len(data) < 3 or data[:2] != _MAGIC:
        raise CrdtError(CrdtErrorKind.CORRUPT, "missing magic bytes")
    if data[2] != _VERSION:
        raise CrdtError(CrdtErrorKind.CORRUPT, f"unsupported version {data[2]}")
    r = _Reader(data, 3)
    count = r.uvarint()
    if count > (len(data) - r.pos) // _MIN_ENCODED_OP:
        raise CrdtError(CrdtErrorKind.CORRUPT, f"count {count} cannot fit in {len(data) - r.pos} bytes")
    ops = []
    for i in range(count):
        kind = r.byte()
        oid, lamport = r.id(), r.uvarint()
        if kind == OpKind.INSERT:
            parent, side, value = r.id(), r.byte(), r.uvarint()
            if value > 0x10FFFF:
                raise CrdtError(CrdtErrorKind.INVALID_OP, f"op {i}: value {value} above U+10FFFF")
            if side not in (0, 1):
                raise CrdtError(CrdtErrorKind.INVALID_OP, f"op {i}: unknown side {side}")
            op = Op(oid, lamport, OpKind.INSERT, parent, Side(side), value)
        elif kind == OpKind.DELETE:
            op = Op(oid, lamport, OpKind.DELETE, target=r.id())
        else:
            raise CrdtError(CrdtErrorKind.CORRUPT, f"unknown op kind {kind} at byte {r.pos - 1}")
        op.validate()
        ops.append(op)
    if r.pos != len(data):
        raise CrdtError(CrdtErrorKind.CORRUPT, f"{len(data) - r.pos} trailing bytes")
    return ops
