# Spec: note link graph algorithms, version 1

This is the normative description of `libs/graph` (ADR-0011). The Go
implementation and the Rust port (`skein-graph`) must return the same results
for the same graph: same sets, same orders, same numbering, and floating-point
values within `1e-9`. Where this document and the code disagree, one of them
has a bug. Each function's doc comment in `libs/graph` repeats the relevant
rule.

## 1. Graph

- **Node:** a non-empty string ID and a `root` flag. IDs compare as byte
  strings (Go `<` on `string`, Rust `Ord` on `str`). "Smallest" always means
  this order.
- **Edge:** `from -> to`, meaning the note `from` links to (cites) `to`, with a
  `weight` (default `0`) used only by the weighted shortest path.
- **Construction** fails, reporting the first offending element in input
  order, with one of these error kinds:

| Kind | When |
|---|---|
| `empty_id` | a node ID is `""` |
| `duplicate_node` | two nodes share an ID |
| `unknown_node` | an edge endpoint is not a node (also: a query names an unknown node) |
| `self_loop` | `from == to` |
| `duplicate_edge` | the same `from -> to` appears twice (`a -> b` and `b -> a` are different edges) |
| `invalid_weight` | a weight is negative, NaN or infinite |
| `invalid_argument` | a query parameter is out of range (not a construction error) |

Node checks run before edge checks; edges are checked in input order for
endpoints, self-loops and weights, and duplicates are reported afterwards in
ascending `(from, to)` order.

- **Undirected view:** `u` and `v` are adjacent when `u -> v` or `v -> u`
  exists. If both exist they form one undirected edge whose weight is the
  smaller of the two. `m` is the number of undirected edges.
- **Results never depend on input order.**

## 2. Directions

| Name | Follows | Question |
|---|---|---|
| `cited_by` (default) | `v` to `u` for each `u -> v` | if this note changes, what is affected |
| `cites` | `u` to `v` for each `u -> v` | what does this note rely on |
| `undirected` | both | what is near this note by links |

## 3. Partitions

Every partition (connected components, strongly connected components, Louvain
communities) is numbered by smallest member: group 0 contains the smallest
node ID, group 1 the smallest ID not in group 0, and so on. Each group's
member list is ascending.

## 4. Algorithms

### 4.1 Distances, shortest path, blast radius

- `distances(source)`: undirected hop distance to every reachable node,
  `source` at 0.
- `shortest_path(from, to)`, undirected, by hops. If `to` is unreachable the
  result is `found = false`, `path = []`, `cost = 0`. Otherwise the path is
  built backwards from `to`: from node `v` at distance `d > 0`, step to the
  smallest-ID neighbour at distance `d - 1`. `cost` is the hop count.
- `blast_radius(source, direction, max_hops)`: hop distance from `source`
  following `direction`, not exploring past `max_hops` (`0` means no limit;
  negative is `invalid_argument`). `ring_sizes[d]` counts nodes at distance
  `d`; `ring_sizes[0] = 1`.

### 4.2 Components and orphans

- Connected components of the undirected view.
- Orphan components: the numbers of components containing no root node,
  ascending. A graph with no roots has every component orphaned.

### 4.3 Strongly connected components

Maximal sets of mutually reachable nodes following links as written,
numbered per section 3.

### 4.4 Cycle

Depth-first search with three colours (white, grey, black). Start nodes are
taken in ascending ID order; each node's out-links are explored in ascending
target order. The first edge found to a grey node `w` closes the cycle: the
search path from `w` to the current node, then `w` again. If there is no
such edge the result is `[]`.

### 4.5 Topological order

Links point at prerequisites: a node may be placed only after every node it
links to. Kahn's algorithm, repeatedly placing the smallest-ID ready node, so
`order` is the lexicographically smallest valid order of the placeable nodes.
`unplaced` lists, ascending, every node that is on a cycle or links,
directly or not, to one. `layers[k]` lists, ascending, the placed nodes whose
layer is `k`: `0` if a node links to nothing, otherwise one more than the
largest layer among its targets.

### 4.6 Weighted shortest path

Undirected, edge cost = weight. Paths compare by total cost, then by hop
count (both exactly, as float64 and integer). If `to` is unreachable, as in
4.1. Otherwise the path is built backwards from `to`: from `v`, step to the
smallest-ID neighbour `u` with `hops(u) = hops(v) - 1` and
`cost(u) + weight(u, v) = cost(v)`, where `cost` and `hops` are the optimal
values from `from`. `semantic_distance(cosine) = 1 - clamp(cosine, -1, 1)`;
cosines more than `1e-9` outside `[-1, 1]`, or NaN, are `invalid_weight`.

### 4.7 Betweenness

Undirected. For each node `v`, the sum over unordered pairs `{s, t}` of other
nodes of `sigma_st(v) / sigma_st` (shortest paths through `v` over all
shortest paths), divided by `(n-1)(n-2)/2`. All zero when `n < 3`.

### 4.8 PageRank

Following links as written, damping `d` in `[0, 1)`, tolerance `> 0`,
`max_iterations >= 1` (otherwise `invalid_argument`). Start from `1/n` each
and iterate

```
r'(v) = (1-d)/n + d * (sum over u -> v of r(u)/outdeg(u)) + d * D/n
```

where `D` is the total rank of nodes with no out-links. Stop after the first
iteration whose L1 change is below the tolerance (`converged = true`) or after
`max_iterations` (`converged = false`). An empty graph returns no scores and
`converged = true`.

### 4.9 Modularity

For a partition given as node -> integer label (every node exactly once,
otherwise `invalid_argument`; unknown IDs are `unknown_node`):

```
Q = sum over communities c of ( L_c/m - (D_c/2m)^2 )
```

with `L_c` the undirected edges inside `c` and `D_c` the sum of undirected
degrees in `c`. `Q = 0` when `m = 0`.

### 4.10 Louvain

Unit weights on the undirected view. Repeat until a level moves no node:

1. Local moving. Every node of the level starts in its own community,
   labelled by the node's position at that level (the level's nodes are in
   ascending order, section 1 at the first level). The label stays with the
   community even after the node that founded it leaves. Visit nodes in
   position order, in passes, until a pass moves nothing. For node `i` with
   degree `k`: remove it from its community, then for each community `c` with
   `w_c` the total edge weight from `i` to members of `c`, and `tot_c` the
   total degree of `c` without `i`, `gain(c) = w_c - tot_c * k / (2m)`.
   Starting from `best = own`, consider neighbouring communities in ascending
   label order and take `c` if `gain(c) > gain(best) + 1e-12`. Put `i` in
   `best`.
2. Aggregation. Renumber communities by smallest position. Each becomes one
   node whose degree is the sum of its members' degrees; the weight between
   two new nodes is the total weight between their members.

The result is the partition of the original nodes (numbered per section 3)
and its modularity per 4.9.

### 4.11 Reading path

The prerequisites of a target are the nodes reachable from it following
`cites`. Steps are the prerequisites plus the target:

1. Group the steps by strongly connected component.
2. Order the groups with Kahn's algorithm on the condensation: a group is
   ready when every group it links to is placed; the ready group with the
   smallest member goes next.
3. Emit each group's members ascending, except that the target is moved to
   the end of its own group (which is always the last group).

For each step: `hops` is its `cites` distance from the target; `via` is the
smallest-ID step at `hops - 1` that links to it (`""` for the target);
`layer` is its group's layer, `0` if the group links to no other group,
otherwise one more than the largest layer among the groups it links to.
`cycles` lists the groups of more than one node, members ascending, ordered
by smallest member.

### 4.12 Diameter

Undirected. `length` is the largest finite hop distance between two
distinct nodes; `from` and `to` (`from < to`) are the lexicographically
smallest pair at that distance. If no two distinct nodes are connected,
`length = 0` and both are `""`.

## 5. Conformance fixtures

`testdata/conformance/graph/*.json` holds shared test cases:

```json
{
  "name": "what the case shows",
  "why": "the reasoning behind every expected result",
  "graph": {
    "nodes": [{"id": "a", "root": true}, {"id": "b"}],
    "edges": [{"from": "a", "to": "b", "weight": 0.5}]
  },
  "build_error": "self_loop",
  "queries": [
    {"query": {"op": "shortest_path", "from": "a", "to": "b"},
     "expected": {"found": true, "path": ["a", "b"], "cost": 1},
     "why": "how this result follows from section 4"}
  ]
}
```

- `root` and `weight` default to `false` and `0`.
- `build_error`, when present, is the error kind construction must fail with,
  and `queries` is empty.
- Only the keys listed in `expected` are compared. Lists compare in order,
  maps by key, numbers within `1e-9`. `[]` and `null` differ: results use
  empty lists, never null.
- `{"error": kind}` as `expected` means the query must fail with that kind.

| `op` | Arguments | Result keys |
|---|---|---|
| `distances` | `source` | `hops` (map) |
| `shortest_path` | `from`, `to` | `found`, `path`, `cost` |
| `weighted_path` | `from`, `to` | `found`, `path`, `cost` |
| `blast_radius` | `source`, `direction`, `max_hops` | `hops` (map), `ring_sizes` |
| `components` | | `of` (map), `members`, `orphans` |
| `scc` | | `of` (map), `members` |
| `cycle` | | `cycle` |
| `topological_order` | | `order`, `unplaced`, `layers` |
| `betweenness` | | `scores` (map) |
| `pagerank` | `damping`, `tolerance`, `max_iterations` | `scores` (map), `converged` |
| `louvain` | | `of` (map), `members`, `modularity` |
| `modularity` | `partition` (map) | `modularity` |
| `reading_path` | `note` | `steps` (list of `note`, `layer`, `hops`, `via`), `cycles` |
| `diameter` | | `length`, `from`, `to` |
