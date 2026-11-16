# Spec: sequence CRDT, version 1

This is the normative description of Skein's replicated text sequence. The Go
implementation (`libs/crdt`) and the Rust port must both follow it exactly:
replicas written in the two languages exchange operations and must reach the
same text. Where this document and the code disagree, one of them has a bug.

## 1. Identifiers

- **Replica ID:** an unsigned 64-bit integer. `0` is reserved and never
  identifies a replica.
- **Operation ID:** the pair `(replica, counter)`. A replica's first
  operation has counter `1`, and each later one is exactly one higher.
- **Root:** the ID `(0, 0)`. It names a sentinel element that holds no
  character and is never deleted.
- **Order:** IDs compare by `replica`, then by `counter`, both ascending.

## 2. Operations

Every operation carries `id`, `lamport` (unsigned 64-bit) and a body.

| Kind | Code | Body |
|---|---|---|
| Insert | `1` | `parent: ID`, `side: Left (0) \| Right (1)`, `value: Unicode scalar value` |
| Delete | `2` | `target: ID` |

An operation is **valid** only if all of these hold. Receivers reject invalid
operations.

- `id.replica != 0` and `id.counter >= 1`.
- Insert: `value` is a Unicode scalar value (`0..=0x10FFFF`, excluding the
  surrogates `0xD800..=0xDFFF`); `parent != id`; not (`parent == Root` and
  `side == Left`); if `parent.replica == id.replica` then
  `parent.counter < id.counter`.
- Delete: `target != Root`, `target.counter >= 1`; if
  `target.replica == id.replica` then `target.counter < id.counter`.

The **Lamport timestamp** of a locally generated operation is one more than
the largest Lamport timestamp the replica has applied. The sequence itself
does not use it for ordering.

## 3. State and ordering

The state is a tree. Each element other than the root has a parent and a side.
Children on each side of a parent are kept sorted ascending by ID.

The **traversal order** of an element `n` is: the traversal of each of `n`'s
left children in ascending ID order, then `n`, then the traversal of each of
`n`'s right children in ascending ID order. The full order is the traversal of
the root. The **text** is the values of the elements in full order that are
not deleted, skipping the root.

**Positions** in local edits count Unicode scalar values (code points) of the
visible text. They never appear in operations.

## 4. Generating a local insert

To insert value `v` so that it follows visible element `L` (or the root, when
inserting at position 0):

1. If `L` has no right children: `parent = L`, `side = Right`.
2. Otherwise: let `R` be the element directly after `L` in the full order
   (deleted elements count). `parent = R`, `side = Left`.

To insert a string, insert its first code point after `L`, then each next code
point after the previous one.

## 5. Generating a local delete

To delete the visible element at position `i`, emit a delete whose target is
that element's ID. To delete `k` elements from position `i`, first collect the
`k` visible elements starting at `i`, then emit one delete per element in that
order.

## 6. Applying an operation

1. If the operation is invalid, reject it.
2. If `id.counter <= V[id.replica]` (already applied): if it equals the
   stored operation with that ID, ignore it; otherwise reject it as an ID
   conflict.
3. If `id.counter != V[id.replica] + 1`, or the operation's dependency (the
   insert's `parent`, the delete's `target`) has not been applied, it is
   **not ready**: change nothing and report it, so the caller can retry later.
4. Insert: add the element as a `side` child of `parent`, in ID order.
   Delete: mark `target` deleted; deleting a deleted element changes nothing
   but the operation is still applied.
5. Set `V[id.replica] = id.counter`, raise the Lamport clock to at least
   `lamport`, and append the operation to the log.

`V` is the **version vector**: for each replica, the counter of its last
applied operation. Because of step 3, operation `(r, c)` has been applied if
and only if `c <= V[r]`. To bring a peer with version vector `P` up to date,
send every logged operation with `counter > P[replica]`, in log order.

## 7. Wire format, version 1

All integers are unsigned LEB128 varints (`uvarint`) in their **shortest**
encoding. Decoders must reject longer encodings, so a batch has exactly one
valid byte representation.

```
batch   = 0x53 0x4B version:u8(=1) count:uvarint op{count}
op      = kind:u8 id.replica:uvarint id.counter:uvarint lamport:uvarint body
insert  = parent.replica:uvarint parent.counter:uvarint side:u8 value:uvarint
delete  = target.replica:uvarint target.counter:uvarint
```

Decoders must also reject: a missing or wrong magic (`"SK"`), an unknown
version or kind, a `count` larger than the remaining bytes divided by 6 (the
smallest encoded operation), truncated input, trailing bytes, and any decoded
operation that is invalid under section 2. A decoder must never crash or
allocate memory in proportion to an unchecked count.

Golden example (from `TestCodecGoldenBytes`):

| Operation | Bytes |
|---|---|
| header, 2 ops | `53 4B 01 02` |
| insert `(1,1)`, lamport 1, right of root, `'h'` | `01 01 01 01 00 00 01 68` |
| delete `(300,2)`, lamport 5, target `(1,1)` | `02 AC 02 02 05 01 01` |

## 8. Conformance fixtures

`testdata/conformance/crdt/*.json` holds shared test cases. Each file is:

```json
{
  "name": "what the case shows",
  "why": "the expected result derived from this spec",
  "replicas": {"A": 1, "B": 2},
  "steps": [
    {"replica": "A", "insert": {"index": 0, "text": "abc"}},
    {"replica": "B", "delete": {"index": 1, "count": 2}},
    {"sync": {"from": "A", "to": "B"}},
    {"sync": "all"},
    {"expect": {"A": "abc", "all": "abc"}}
  ],
  "expect": {"all": "abc"}
}
```

- `replicas` maps names to replica IDs.
- `insert` and `delete` are local edits on the named replica (positions in
  code points).
- `sync {from, to}` applies `from`'s operations missing at `to`, using
  section 6's rule.
- `sync "all"` visits replicas in ascending name order as receivers and, for
  each, applies every other replica's missing operations in ascending name
  order.
- `expect` maps replica names (or `"all"`) to the exact expected text.

## 9. Invariants

Implementations must be able to check these at any time (Go:
`Sequence.CheckInvariants`), and their tests must check them after every
operation.

- I1 The root has ID Root, no parent, no left children, and is first in order.
- I2 Each child's recorded parent and side match where it is stored.
- I3 Children on each side are strictly ascending by ID.
- I4 Any secondary order structure (the Go linked list) equals the traversal.
- I5 The cached visible length equals the count of visible elements.
- I6 For each replica, the log holds counters `1..=V[r]` once each, in order.
- I7 Every insert in the log has an element; the Lamport clock is at least
  every logged timestamp.

## 10. Planned extensions (not version 1)

- Block markers (one marker element per block, carrying last-writer-wins
  attributes ordered by `(lamport, replica)`), for the block model.
- A restore operation, to undo a delete without inserting copies.
- Run-length encoding of consecutive operations from one replica. This will be
  wire format version 2; version 1 decoders reject it by its version byte.
