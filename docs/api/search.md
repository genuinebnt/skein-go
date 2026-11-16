# Search

`SearchService` ([`search.proto`](../../proto/skein/v1/search.proto)): text,
regular-expression and code search, and title suggestions
([ADR-0010](../adr/0010-search-and-discovery.md)). Results only include notes
the caller can read. Requests count against the search rate-limit budget.
Semantic search is in [ml.md](ml.md). Conventions are in [README.md](README.md).

| Method | Path | RPC |
|---|---|---|
| `GET` | `/v1/workspaces/{workspaceId}/search` | Search |
| `GET` | `/v1/workspaces/{workspaceId}/title-suggestions` | SuggestTitles |

### Search

`GET /v1/workspaces/{workspaceId}/search`. Query parameters:

| Parameter | Meaning |
|---|---|
| `query` | Required. Words in text mode; an [RE2](https://github.com/google/re2/wiki/Syntax) expression in regex mode |
| `mode` | `SEARCH_MODE_TEXT` (default) or `SEARCH_MODE_REGEX` |
| `caseSensitive`, `wholeWord` | Booleans, default false |
| `languages` | Repeat for each: only code blocks in these languages |
| `codeOnly` | Only code blocks, any language |
| `page.pageSize`, `page.pageToken` | Pagination |

Text mode is typo-tolerant. Regex mode uses the trigram index to choose
candidate blocks and runs the expression on those; `candidates` and
`matched` show how selective the index was for the query. An expression
that does not compile is `INVALID_ARGUMENT` with a `BadRequest` on `query`.

```
GET /v1/workspaces/01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f/search?query=func%20%5Cw%2B%5C(ctx%20context%5C.Context&mode=SEARCH_MODE_REGEX&languages=go&page.pageSize=20
```

`snippet` is a window of the block's text; `highlights` are code-point
ranges inside the snippet, and `location` points at the match in the block.

```json skein.v1.SearchResponse
{
  "hits": [
    {
      "location": {
        "noteId": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
        "blockId": "4831727201934127107:23",
        "end": 33
      },
      "noteTitle": "Raft log compaction",
      "snippet": "func Snapshot(ctx context.Context, upTo uint64) error {",
      "highlights": [{"end": 33}],
      "score": 1,
      "language": "go"
    }
  ],
  "page": {},
  "candidates": 14,
  "matched": 1
}
```

### SuggestTitles

`GET /v1/workspaces/{workspaceId}/title-suggestions`. Typo-tolerant title
completion for the command palette and `[[links]]`. Results are ordered by
edit distance, then by title.

```
GET /v1/workspaces/01936f3e-1a2b-7c3d-8e4f-5a6b7c8d9e0f/title-suggestions?prefix=raft%20lgo&limit=5
```

```json skein.v1.SuggestTitlesResponse
{
  "suggestions": [
    {
      "noteId": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
      "title": "Raft log compaction",
      "editDistance": 1
    },
    {
      "noteId": "01936f42-4d5e-7f60-9b72-8c9d0e1f2031",
      "title": "Raft leader election",
      "editDistance": 2
    }
  ]
}
```
