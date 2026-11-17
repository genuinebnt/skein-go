# Fugue by hand

A worked example of the rules in
[`docs/spec/crdt-sequence.md`](../../../spec/crdt-sequence.md) sections 3 and
4, for working through `libs/crdt/sequence.go` before porting it. Do these on
paper first; each one is also a small Rust unit test.

Notation: an element is `value(replica,counter)`. `R` and `L` after an
element mean "right child of" and "left child of". The root is `^`.

## The two rules

Traversal of a node `n`: each left child of `n` (ascending ID), then `n`,
then each right child (ascending ID). The text is the traversal of the root
without deleted elements.

To insert after visible element `L` (the root at position 0):

1. `L` has no right children: the new element is a right child of `L`.
2. Otherwise: let `R` be the element right after `L` in the full order,
   tombstones included. The new element is a left child of `R`.

In case 2, `R` is the first node of `L`'s first right subtree, so `R` has no
left children before this insert (or only concurrently inserted ones). That
is why the new element lands exactly between `L` and `R`.

## 1. Typing at the end

Replica 1 types `ab` into an empty note.

- `a(1,1)`: the root has no right children, so `a` is `R ^`.
- `b(1,2)`: `a` has no right children, so `b` is `R a`.

```
^
└─R a(1,1)
     └─R b(1,2)
```

Traversal: `^ a b`. Every forward-typed character is the right child of
the one before, so a long note is a long chain. That chain is why the walks
must be iterative (see `TestCheckInvariantsWalksAHundredThousandDeepChainOnASmallStack`).

## 2. Inserting in the middle

Still on replica 1, insert `c` at position 1 (between `a` and `b`).

- `L = a`. `a` has a right child (`b`), so case 2: `R` is the element after
  `a` in the full order, which is `b`.
- `c(1,3)` becomes `L b`.

```
^
└─R a(1,1)
     └─R b(1,2)
          └─L c(1,3)
```

Traversal of `b`: its left children first (`c`), then `b`. Text: `acb`.

## 3. Concurrent typing at the same place (no interleaving)

Replicas 1 and 2 both start from an empty note. Replica 1 types `ab`,
replica 2 types `xy`, both at position 0, then they sync.

- Replica 1 makes `a(1,1) R ^` and `b(1,2) R a`.
- Replica 2 makes `x(2,1) R ^` and `y(2,2) R x`.

After sync the root has two right children, sorted by ID: `(1,1)` before
`(2,1)`.

```
^
├─R a(1,1)
│    └─R b(1,2)
└─R x(2,1)
     └─R y(2,2)
```

Traversal: `^`, then the whole subtree of `a` (`a b`), then the whole
subtree of `x` (`x y`). Text on both replicas: `abxy`. Each person's run
stays together because it is one subtree; this is the non-interleaving
property. Fixture `001-concurrent-forward-typing` pins it.

RGA with a Lamport tie-break would also give a contiguous result here; the
cases where RGA interleaves and Fugue does not involve backward typing (the
cursor staying in front of what was just typed). Work through fixture
`006-concurrent-backward-typing` the same way: each new character is
inserted after the same `L`, and case 2 keeps making it a left child of the
previous one, so each person's run is again one subtree.

## 4. Concurrent inserts between the same two characters

Both replicas have `ab` from example 1. Concurrently, replica 1 inserts `c`
and replica 2 inserts `d`, both at position 1.

- Each sees `L = a` with right child `b`, so each makes its character a
  left child of `b`: `c(1,3) L b`, `d(2,1) L b`.
- After sync, `b`'s left children sorted by ID: `(1,3)`, `(2,1)`.

Text: `a c d b`, on both replicas. Fixture `002-concurrent-insert-in-middle`.

## 5. Deletes are tombstones

Delete `b` from `acb` (example 2). `b` is marked deleted and stays in the
tree, because a concurrent insert may have used it as a parent (as `c` did).
Text: `ac`. Inserting at position 2 now: `L = c`; `c` has no right
children, so the new element is `R c`, and it sorts after `c` and before
the tombstone `b` in the full order. Fixtures
`003-insert-next-to-concurrent-delete` and
`004-concurrent-delete-same-character` cover the concurrent versions.

## 6. Causality

Replica 2 receives `b(1,2)` before `a(1,1)`. `Apply` returns `NotReady`
(spec section 6, step 3: the counter is not `V[1] + 1`, and the parent is
unknown) and changes nothing. A `CausalBuffer` keeps `b`, applies `a` when
it arrives, then retries and applies `b`. Delivering `a` again later is a
no-op (step 2). `TestConvergenceUnderReorderingAndDuplication` does this at
random with many replicas.

## From the drawing to the Rust structs

The tree above is the `parent`, `side`, `left` and `right` fields of each
node; the linked list (`prev`, `next`) is the traversal written out, kept so
reading the text and finding "the element after `L`" are cheap. In Rust the
pointers become indexes into one arena:

```rust
struct NodeIdx(u32);
struct Node {
    id: Id,
    value: char,          // unused for the root
    deleted: bool,
    parent: Option<NodeIdx>,
    side: Side,
    left: Vec<NodeIdx>,   // ascending by id (I3)
    right: Vec<NodeIdx>,  // ascending by id (I3)
    prev: Option<NodeIdx>,
    next: Option<NodeIdx>,
}
```

Inserting a child: binary search the sibling vector by ID
(`insertSorted`), then link the new node into the list right before the
first node of the next sibling's subtree, or right after the last node of
the previous sibling's subtree (or of the parent's left side), which is
what `firstInSubtree`, `lastInSubtree`, `linkBefore` and `linkAfter` do.
Check I4 (the list equals the traversal) after every operation while you
get this right.
