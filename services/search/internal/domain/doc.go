// Package domain is the search service's read model and the algorithms
// that query it, with no I/O: the projection of documents and identity
// events into note states, the per-workspace indexes (block trigrams,
// title trigrams, a word index for ranking), text, regex and title search,
// and the readable link graph.
//
// # Projection
//
// Events become Mutations (one change to one note). ApplyMutation is a
// pure, idempotent function from a note's state and a mutation to its next
// state; the service stores note states durably and appends each changed
// note to a change feed. A Projection follows that feed: it is given the
// current state of each changed note and brings its indexes up to date by
// difference, so a block edit re-indexes one block. Purged notes become
// tombstones that ignore every later event, so replaying old events can
// never bring erased text back.
//
// # Search
//
//   - Text: terms must all occur in a note (title or any allowed block),
//     exactly or within a typo budget (TypoBudget, Damerau distance).
//     Misspellings are found through a trigram index over the vocabulary,
//     filtered by banded Levenshtein and confirmed by Damerau distance
//     (libs/dp). Notes are ranked by BM25 over the readable notes, with the
//     title weighted TitleWeight times, boosted when every term is in the
//     title or in one block.
//   - Regex: the readable blocks, as a bitmap, are intersected with the
//     pattern's trigram candidates (libs/trigram) and confirmed by the
//     regexp.
//   - Titles: trigram candidates from a title index, decided by the
//     Damerau distance to the title's closest prefix.
//
// Permission filtering always comes first: every function takes the
// caller's Readable set and never reads, ranks or counts a note outside
// it.
//
// # Invariants
//
// Workspace.CheckInvariants (S1-S5), WordIndex.CheckInvariants (W1-W3) and
// Projection.CheckInvariants (P1) are called by the tests after every
// change.
//
// # Porting notes
//
// Pure Go over libs/trigram, libs/dp and libs/graph, single-threaded,
// results sorted explicitly wherever map iteration could leak in. Mutation
// and MutationKind become an enum; the snapshot format (snapshot.go) is
// fixed-width little-endian with minimal varints.
package domain
