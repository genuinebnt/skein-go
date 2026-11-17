# Porting `libs/graph` to Rust (`skein-graph`)

The note link graph and the algorithms the search service runs on it:
traversal and blast radius, connected and strongly connected components,
cycles, topological order, weighted (semantic) shortest paths, betweenness,
PageRank, Louvain communities and modularity, reading paths and diameter.
Every result is deterministic, including numbering and tie-breaks, so Go
and Rust agree on sets, orders and (within `1e-9`) floating-point values.

Normative spec: [`docs/spec/graph.md`](../../docs/spec/graph.md).
Decisions: [ADR-0011](../../docs/adr/0011-graph-and-dp-libraries.md),
[ADR-0010](../../docs/adr/0010-search-and-discovery.md) (link distance as a
discovery signal).
General rules: [`docs/porting-to-rust.md`](../../docs/porting-to-rust.md)
(section 5 has graph-specific notes).

## 1. What it is and why Skein needs it

Notes link to each other with `[[wiki links]]`. The search service projects
those links (from `LinksChanged` events, built with `Links` in `libs/blocks`) into a graph
per workspace and answers questions about it. An edge `from -> to` means
`from` cites `to`; links point at prerequisites.

| Feature | Algorithm | RPC (`proto/skein/v1/search.proto`) |
|---|---|---|
| "If I change this note, what is affected?" | BFS following `cited_by` (the default) or `cites`, by hop rings | `BlastRadius` |
| Shortest link path between two notes, by hops or by semantic distance | BFS with the walk-back tie rule; Dijkstra on `1 - cosine` | `ShortestPath` |
| Graph health: islands, orphans, citation loops, a valid reading order, central notes, communities, the longest shortest path | connected components and orphans, Tarjan SCC, three-colour cycle search, Kahn with layers, PageRank, Louvain and modularity, Brandes betweenness, diameter | `AnalyzeGraph` |
| What to read before a note, in order | prerequisites by `cites`, SCC condensation, Kahn on groups | `ReadingPath` |
| Group a topic's notes before planning | SCC on the topic's subgraph | `ReadingPlan` (with `libs/dp`) |
| Discovery's "link distance" signal | undirected hop distance | discovery views (ADR-0010) |

Call sites: `services/search/internal/app/analysis.go` (AnalyzeGraph),
`internal/app/graph.go` (blast radius, paths, reading path and plan),
`internal/domain/workspace.go` (building the graph from the projection),
`internal/adapters/grpc/server.go`.

## 2. Learn first

| # | Source | What to take from it | Maps to | Time |
|---|---|---|---|---|
| 1 | Robert Tarjan, ["Depth-First Search and Linear Graph Algorithms"](https://sites.cs.ucsb.edu/~gilbert/cs240a/notes/TarjanDFS.pdf) (SIAM J. Comput. 1(2), 1972) | Low-link values and the SCC stack. Tarjan emits components in reverse topological order; Skein renumbers them by smallest member afterwards | `components.go` (`tarjan`, `canonicalLabels`) | 2 h |
| 2 | David J. Pearce, ["A space-efficient algorithm for finding strongly connected components"](https://whileydave.com/publications/pea16_ipl/) (IPL 116(1), 2016) | A cleaner formulation that is easy to write with an explicit stack; useful when you make Tarjan iterative in Rust | `components.go` | 1 h (optional) |
| 3 | [Tarjan's SCC algorithm](https://en.wikipedia.org/wiki/Tarjan%27s_strongly_connected_components_algorithm) (Wikipedia) | The recursive pseudocode to turn into frames, and the reverse-topological output order | `components.go` | 0.5 h |
| 4 | A. B. Kahn, ["Topological sorting of large networks"](https://scilit.com/publications/1bc5bd2b4676f7e4e83f2fd8d23f7aaf) (CACM 5(11), 1962) | In-degree counting and a ready set; with a min-heap as the ready set the order is the lexicographically smallest | `topo.go`, `readingpath.go` | 0.5 h |
| 5 | E. W. Dijkstra, ["A Note on Two Problems in Connexion with Graphs"](https://eudml.org/doc/131436) (Numerische Mathematik 1, 1959) | The shortest-path algorithm; Skein compares `(cost, hops)` lexicographically and rebuilds the path with a stated tie rule | `dijkstra.go` | 0.5 h |
| 6 | Ulrik Brandes, "A faster algorithm for betweenness centrality" (J. Math. Sociology 25(2), 2001; [algorithm summary](https://en.wikipedia.org/wiki/Brandes%27_algorithm)) | One BFS per source counting shortest paths (`sigma`), then dependency accumulation in reverse BFS order; halve for undirected graphs. Skein then divides by `(n-1)(n-2)/2` | `centrality.go` (`Betweenness`) | 1.5 h |
| 7 | Page, Brin, Motwani, Winograd, "The PageRank Citation Ranking: Bringing Order to the Web" (Stanford InfoLab technical report, 1999; [ilpubs.stanford.edu:8090/422](http://ilpubs.stanford.edu:8090/422/)) | Power iteration with damping, and dangling nodes (Skein spreads their rank evenly, spec 4.8) | `centrality.go` (`PageRank`) | 1 h |
| 8 | M. E. J. Newman and M. Girvan, ["Finding and evaluating community structure in networks"](https://arxiv.org/pdf/cond-mat/0308217) (Phys. Rev. E, 2004) | Modularity `Q`; Skein's formula in spec 4.9 is the same quantity written with `L_c` and `D_c` | `community.go` (`Modularity`) | 1 h |
| 9 | Blondel, Guillaume, Lambiotte, Lefebvre, ["Fast unfolding of communities in large networks"](https://arxiv.org/abs/0803.0476) (J. Stat. Mech., 2008) | Louvain's two phases: local moving by modularity gain, then aggregation into a community graph, repeated | `community.go` (`Louvain`, `localMoving`, `aggregate`) | 1.5 h |
| 10 | Traag, Waltman, van Eck, ["From Louvain to Leiden: guaranteeing well-connected communities"](https://arxiv.org/abs/1810.08473v3) (Sci. Rep., 2019) | Louvain's known flaw (badly connected or disconnected communities) and the fix; context for a later improvement, not part of the port | `community.go` | 1 h (optional) |
| 11 | [NetworkX Louvain source](https://networkx.org/documentation/stable/_modules/networkx/algorithms/community/louvain.html) | A readable reference implementation. It shuffles nodes with a seed; Skein visits nodes in position order and keeps labels with communities (spec 4.10), so do not copy its order | `community.go` | 0.5 h |
| 12 | [petgraph](https://docs.rs/crate/petgraph/latest/source/src/algo/mod.rs) (`tarjan_scc`, `toposort`, `dijkstra`, `page_rank`) and [rustworkx-core](https://docs.rs/rustworkx-core/) (`betweenness_centrality`) | Rust implementations to use as test oracles; their tie-breaks and numbering differ from the spec | tests | browse |

Reading order: 3, 1, 4 (components and order, an evening), 5, 6, 7 (paths
and centrality), 8, 9 (communities), then 2, 10, 11, 12 as needed. About
10 hours.

## 3. Port order

| # | Go file | Contents | Needs | Invariants and rules | Done when |
|---|---|---|---|---|---|
| 1 | `doc.go` | model, directions and views, determinism, G1–G5, iteration not recursion | none | | `lib.rs` `//!` docs |
| 2 | `errors.go` | `ErrEmptyID`, `ErrDuplicateNode`, `ErrUnknownNode`, `ErrSelfLoop`, `ErrDuplicateEdge`, `ErrInvalidWeight`, `ErrInvalidArgument`, `ErrInvariant` | none | kinds are the spec's error names | the enum in section 5 |
| 3 | `graph.go` | `Node`, `Edge`, `Graph` (sorted `ids`, `index`, `root`, `out`/`in` with weights, `adj` undirected with min weight, edge counts), `New` with validation order, `mergeUndirected`, accessors, `Neighbours`, `String` | 2 | construction order of checks (spec 1): nodes first, then edges in input order, duplicates last in `(from, to)` order | `new_rejects_invalid_input`, `new_accepts_links_both_ways_as_two_edges`, `empty_graph_answers_everything_with_empty_results`; fixtures 001–003 |
| 4 | `invariants.go` | `CheckInvariants`: G1–G5 | 3 | G1–G5 | `check_invariants_detects_corruption` (an in-crate unit test: it corrupts private fields) |
| 5 | `traverse.go` | `Direction`, `neighbours`, `bfs` with `max_hops`, `Distances`, `Path`, `ShortestPath` with `closerNeighbour` (walk back to the smallest-ID neighbour one hop closer), `Rings`, `BlastRadius` | 3 | spec 4.1 | `neighbours_follow_the_requested_direction`, `queries_reject_unknown_nodes_and_bad_arguments`, `distances_match_matrix_breadth_first_search`, `shortest_path_is_shortest_and_follows_the_tie_rule`, `blast_radius_matches_exhaustive_reachability`; fixtures 007, 008 |
| 6 | `components.go` | `Partition`, `canonicalLabels` (number by smallest member), `ConnectedComponents`, `OrphanComponents`, `StronglyConnected` with iterative `tarjan` | 3 | spec 3 and 4.2–4.3 | `connected_components_match_union_find`, `strongly_connected_matches_kosaraju_and_is_maximal`; fixtures 005, 006 |
| 7 | `cycle.go` | `FindCycle`: three colours, `searchFrame{node, next}`, `closeCycle` | 3 | spec 4.4 | `find_cycle_ignores_a_diamond`, `find_cycle_starts_at_the_target_of_the_first_back_edge`, `find_cycle_returns_a_real_cycle_exactly_when_one_exists`; fixture 004 |
| 8 | `topo.go` | `Order` (order, unplaced, layers), `TopologicalOrder` with a min-heap | 3 | spec 4.5 | `topological_order_is_the_smallest_valid_order`; fixtures 004, 006 |
| 9 | `dijkstra.go` | `SemanticDistance`, `weightedDistances` (cost and hops), `WeightedDistances`, `ShortestWeightedPath`, `costItem`/`costHeap` | 3, 5 | spec 4.6, compare `(cost, hops, node)` | `semantic_distance_clamps_rounding_and_rejects_nonsense`, `shortest_weighted_path_prefers_fewer_hops_when_costs_tie`, `weighted_distances_match_floyd_warshall`; fixtures 009, 013 |
| 10 | `centrality.go` | `Betweenness` (Brandes, undirected), `PageRankOptions`, `DefaultPageRankOptions`, `Ranks`, `PageRank` | 3, 5 | spec 4.7–4.8, summation order | `betweenness_of_a_star_centre_is_one`, `page_rank_solves_the_dangling_star_exactly`, `page_rank_reports_when_it_runs_out_of_iterations`, `betweenness_matches_pair_counting`, `page_rank_matches_high_precision_power_iteration`; fixture 010 |
| 11 | `community.go` | `Modularity`, `labelsFrom`, `modularityOf`, `Communities`, `minGain = 1e-12`, `Louvain`, `communityGraph`, `localMoving`, `aggregate` | 3, 6 | spec 4.9–4.10, the label rule | `louvain_recovers_planted_communities`, `louvain_leaves_isolated_notes_alone`, `modularity_matches_the_double_sum`, `louvain_is_consistent_and_never_beats_the_optimum`; fixtures 011, 012 |
| 12 | `diameter.go` | `Diameter` (BFS from every node) | 5 | spec 4.12 | `diameter_matches_all_pairs` |
| 13 | `readingpath.go` | `ReadingStep`, `Reading`, `ReadingPath`, `targetLast`, `via` | 5, 6, 8 | spec 4.11 | `reading_path_of_a_note_without_links_is_itself`, `reading_path_puts_prerequisites_first` |
| 14 | tests: `helpers_test.go`, `conformance_test.go`, `oracle_test.go`, `property_test.go` | the spec-string graph builder, random graphs, brute-force oracles (matrix BFS, union-find, Kosaraju, Floyd–Warshall, pair counting, high-precision power iteration, exhaustive modularity), the fixture runner | all | `results_do_not_depend_on_input_order` | every file in `testdata/conformance/graph/` |
| 15 | `deep_test.go` | 100,000-note chain and ring under a capped stack | 6, 7, 8, 13 | no recursion | `deep_chain_needs_no_deep_stack` passes on the default 2 MiB test thread |
| 16 | fuzz: `fuzz_test.go` | `FuzzAlgorithmsOnArbitraryGraphs` | all | every algorithm runs and invariants hold on arbitrary input | `cargo fuzz run algorithms` |
| 17 | benchmarks: `bench_test.go` | the 10,000-note workload in `docs/PERF.md` | all | | criterion numbers beside Go's |

Fixtures (`testdata/conformance/graph/`): construction errors 001–003,
cycles and order 004 and 006, orphans 005, blast radius 007, path ties 008,
semantic paths 009 and 013, centrality and rank 010, communities 011 and
012. Only keys listed in `expected` are compared; `[]` and `null` differ,
so results must use empty vectors, never `Option<Vec<_>>`.

## 4. Dependencies

Imports: Go standard library only (`slices`, `math`, `container/heap`,
`errors`, `fmt`, `strings`).

| Need | Rust | Note |
|---|---|---|
| errors | `thiserror` | |
| id lookup | `HashMap<String, usize>` | lookup only |
| results keyed by note | `BTreeMap<String, T>` | deterministic printing in tests; Go maps here are never iterated for output |
| heaps | `BinaryHeap<Reverse<usize>>` (Kahn); a `CostItem` with a manual `Ord` for Dijkstra | see section 6 |
| graph library | none: write it | `petgraph` has its own node indexes, numbering and tie-breaks; the spec's results would need a wrapper as big as the port. Use petgraph and rustworkx-core only in tests as oracles |
| property tests, fuzzing, benchmarks | `proptest`, `cargo-fuzz`, `criterion` | |

What depends on it: the search service crate only.

## 5. Rust layout

Crate `skein-graph`, edition 2024, `#![forbid(unsafe_code)]`. Node
positions are `usize` and never leave the crate; the API speaks note IDs.

```
skein-graph/
  src/
    lib.rs          //! from doc.go
    error.rs
    graph.rs        Graph, Node, Edge, new, accessors
    invariants.rs
    traverse.rs     Direction, bfs, distances, shortest_path, blast_radius
    components.rs   Partition, connected components, orphans, Tarjan
    cycle.rs
    topo.rs
    dijkstra.rs
    centrality.rs   betweenness, page_rank
    community.rs    modularity, louvain
    diameter.rs
    reading_path.rs
  tests/{conformance.rs, oracle.rs, property.rs, deep.rs, ...}
  fuzz/fuzz_targets/algorithms.rs
  benches/graph.rs
```

Public API sketch:

```rust
pub struct Node { pub id: String, pub root: bool }
pub struct Edge { pub from: String, pub to: String, pub weight: f64 }

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Direction { #[default] CitedBy, Cites, Undirected }

pub struct Graph { /* ids, index, root, out, out_weight, in_, adj, adj_weight, edge_count, undirected_edge_count */ }

pub struct Path { pub found: bool, pub nodes: Vec<String>, pub hops: usize, pub cost: f64 }
pub struct Rings { pub hops: BTreeMap<String, usize>, pub sizes: Vec<usize> }
pub struct Partition { pub of: BTreeMap<String, usize>, pub members: Vec<Vec<String>> }
pub struct Order { pub order: Vec<String>, pub unplaced: Vec<String>, pub layers: Vec<Vec<String>> }
pub struct PageRankOptions { pub damping: f64, pub tolerance: f64, pub max_iterations: usize }
pub struct Ranks { pub scores: BTreeMap<String, f64>, pub iterations: usize, pub converged: bool }
pub struct Communities { pub partition: Partition, pub modularity: f64 }
pub struct ReadingStep { pub note: String, pub layer: usize, pub hops: usize, pub via: String }
pub struct Reading { pub steps: Vec<ReadingStep>, pub cycles: Vec<Vec<String>> }
pub struct Diameter { pub length: usize, pub from: String, pub to: String }

impl Graph {
    pub fn new(nodes: &[Node], edges: &[Edge]) -> Result<Self, Error>;
    pub fn len(&self) -> usize;
    pub fn edge_count(&self) -> usize;
    pub fn undirected_edge_count(&self) -> usize;
    pub fn has(&self, id: &str) -> bool;
    pub fn nodes(&self) -> &[String];
    pub fn roots(&self) -> Vec<String>;
    pub fn edges(&self) -> Vec<Edge>;
    pub fn neighbours(&self, id: &str, dir: Direction) -> Result<Vec<String>, Error>;
    pub fn check_invariants(&self) -> Result<(), Error>;

    pub fn distances(&self, source: &str) -> Result<BTreeMap<String, usize>, Error>;
    pub fn shortest_path(&self, from: &str, to: &str) -> Result<Path, Error>;
    /// max_hops 0 means no limit; Go's negative max_hops (invalid_argument) cannot be expressed.
    pub fn blast_radius(&self, source: &str, dir: Direction, max_hops: usize) -> Result<Rings, Error>;
    pub fn connected_components(&self) -> Partition;
    pub fn orphan_components(&self) -> Vec<usize>;
    pub fn strongly_connected(&self) -> Partition;
    pub fn find_cycle(&self) -> Vec<String>;
    pub fn topological_order(&self) -> Order;
    pub fn weighted_distances(&self, source: &str) -> Result<BTreeMap<String, f64>, Error>;
    pub fn shortest_weighted_path(&self, from: &str, to: &str) -> Result<Path, Error>;
    pub fn betweenness(&self) -> BTreeMap<String, f64>;
    pub fn page_rank(&self, opts: PageRankOptions) -> Result<Ranks, Error>;
    pub fn modularity(&self, assignment: &HashMap<String, i64>) -> Result<f64, Error>;
    pub fn louvain(&self) -> Communities;
    pub fn reading_path(&self, note: &str) -> Result<Reading, Error>;
    pub fn diameter(&self) -> Diameter;
}

pub fn semantic_distance(cosine: f64) -> Result<f64, Error>;
```

If the service passes `max_hops` from a protobuf `int32`, keep the
conversion (and the `invalid_argument` for negatives) in the service, or
take `i64` here to keep the Go contract and the fixture that checks it.

Error enum (one variant per spec kind, carrying the offending element):

```rust
#[derive(Debug, thiserror::Error, PartialEq)]
pub enum Error {
    #[error("graph: empty node id")]                  EmptyId,
    #[error("graph: duplicate node id {0:?}")]        DuplicateNode(String),
    #[error("graph: unknown node {0:?}")]             UnknownNode(String),
    #[error("graph: self-loop on {0:?}")]             SelfLoop(String),
    #[error("graph: duplicate edge {from:?} -> {to:?}")] DuplicateEdge { from: String, to: String },
    #[error("graph: invalid weight {weight} on {from:?} -> {to:?}")] InvalidWeight { from: String, to: String, weight: f64 },
    #[error("graph: invalid argument: {0}")]          InvalidArgument(String),
    #[error("graph: invariant violated: {0}")]        Invariant(String),
}
```

The fixture runner maps variants to the spec names (`empty_id`,
`duplicate_node`, ...). `Direction`'s "unknown direction" error disappears
with the enum.

Feature flags: none. WASM: not used by the browser; the crate stays
WASM-clean (it is in `WASM_LIBS` in the `Makefile`), so a future offline
graph view can use it.

## 6. Pitfalls and cross-checking

**Recursion.** Tarjan, the cycle search and reading paths must use explicit
frame stacks. A 100,000-note chain overflows a recursive Rust DFS on a
2 MiB thread; `TestDeepChainNeedsNoDeepStack` caps Go's stack at 4 MiB to
prove the Go code does not recurse, and the Rust test should run on the
default thread with no `stack_size` override.

**Tie-breaks are results.** Nodes are stored in ascending byte order, and
every loop visits nodes and neighbours in that order. Rust's `str` `Ord` is
byte order, the same as Go's string `<`. Partitions are numbered by
smallest member, never by discovery order (Tarjan finds components in
reverse topological order; renumber them).

**Floating point.** Betweenness, PageRank, modularity and path costs must
add the same values in the same order as Go so that results agree to the
last bit where they can: PageRank pulls from in-links in ascending order,
modularity sums communities in label order, Louvain's gain compares with
`gain(c) > gain(best) + 1e-12` over neighbouring communities in ascending
label order. Do not reach for `iter().sum()` over a `HashMap` or for
parallel iterators here. Fixtures still allow `1e-9`.

**Dijkstra's heap.** `f64` is not `Ord`. Give `CostItem { cost, hops, node }`
a manual `Ord` comparing `(cost, hops, node)` with `f64::total_cmp`, and
wrap it in `Reverse` for a min-heap. Costs are never NaN (construction
rejects them), so `total_cmp` agrees with `<`.

**Weights.** The undirected view keeps the smaller weight when both
directions exist; `SemanticDistance` clamps cosines within `1e-9` of
`[-1, 1]` and rejects the rest.

**Integer widths.** Positions and counts are `usize`; nothing here is a wire
format. Diameter's BFS from every node is O(V·E) and allocates heavily in
Go (4.4 GB per op at 10,000 notes in `docs/PERF.md`); reuse the distance
buffer across sources in Rust.

**Empty results.** `[]` and `null` differ in fixtures. Return empty `Vec`s
and empty maps.

**Map iteration.** Go uses maps only in results and accumulators whose keys
are sorted before use. In Rust, prefer `BTreeMap` for any map that is
printed or compared, so tests are deterministic without sorting.

Cross-checking against Go:

1. **Fixtures.** `tests/conformance.rs` runs `testdata/conformance/graph/*.json`
   as spec section 5 describes (only listed keys compared, numbers within
   `1e-9`, `{"error": kind}` for failures).
2. **Oracles.** Port `oracle_test.go` and `property_test.go` with their
   brute-force oracles; add petgraph's `kosaraju_scc` and `toposort`, and
   rustworkx-core's betweenness, as extra oracles for sets and values
   (compare as sets, not orders).
3. **Input order.** Port `results_do_not_depend_on_input_order`: shuffle
   nodes and edges and compare every result exactly.
4. **Differential runs.** The 10,000-note benchmark graph (seed 1) is a
   good corpus: dump each algorithm's result as JSON from Go once
   (committed under `testdata/crosscheck/graph/`) and compare in Rust,
   values within `1e-9`. This exercises Louvain's aggregation levels and
   PageRank's summation order far beyond the small fixtures.
