# 0010. Trigram and regex code search; unblended discovery signals

Date: 2026-11-16. Status: accepted.

## Decision
- `search` keeps a trigram inverted index over block text with roaring bitmap
  posting lists. Regex queries use Russ Cox's method (as in Google Code
  Search and zoekt): turn the regex into a boolean trigram query, take the
  candidate blocks, then confirm with the real regex. Go's `regexp` and Rust's
  `regex` are both RE2-style, so semantics port.
- Code blocks are indexed with their language for filters such as `lang:go`,
  case sensitivity and whole-word matching.
- Fuzzy title search uses trigram candidates re-ranked by edit distance.
- Permission filtering intersects the user's readable notes (a bitmap) with
  the posting lists before ranking.
- `ml` owns semantic search. Filters (permissions, topics, tags) are applied
  during the HNSW descent, not after it, so a narrow filter does not collapse
  recall. Every approximate query can run an exact scan beside it and report
  recall.
- Discovery shows three separate signals for related notes, never one blended
  score: meaning (cosine), shared tags (count and Jaccard) and link distance
  in hops, with "not connected" as its own value. The rows where the signals
  disagree become link suggestions.

## Notes
The unblended signals, filtering during the descent, and the measured recall
are carried over from the Marginal project, where they proved their worth.
