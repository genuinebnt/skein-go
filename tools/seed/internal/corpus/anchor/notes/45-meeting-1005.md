---
title: Meeting notes 2026-10-05 search review
author: owner
editors: storage, ml
tags: meeting, search
created: 42
acl: guest=commenter
---
Attendees: Mira, Kenta, Ottilie, Ifeoma (guest, from the design agency). Notes by Mira.

## Decisions

- Adopt a trigram index for regex and code search, with queries of at least 3 characters.
- Kenta owns the trigram index; target date 2026-10-24.
- Semantic results stay a separate panel; no blended score (see [[Hybrid retrieval with BM25]]).
- Ifeoma will mock up the search results page with code snippets highlighted.

## Open questions

- Do we index trashed notes for their owners? Leaning no.
- How do typo-tolerant title suggestions rank exact prefix matches against close misspellings?

> Search that ignores permissions is a data leak with a nice UI.

Follow-ups go in [[Ideas backlog]].
