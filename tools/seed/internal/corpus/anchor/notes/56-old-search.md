---
title: Old search design (archived)
author: owner
tags: search, archived
created: 244
trashed: true
---
Superseded by the trigram index and the semantic panel. Kept in the trash for reference until it is purged.

The old design used Postgres full-text search only, with no code search and no typo tolerance. Ranking was ts_rank with title weight A and body weight D.

```sql
SELECT id, title, ts_rank(search, q) AS rank
FROM notes, plainto_tsquery('english', $1) AS q
WHERE search @@ q
ORDER BY rank DESC
LIMIT 20;
```
