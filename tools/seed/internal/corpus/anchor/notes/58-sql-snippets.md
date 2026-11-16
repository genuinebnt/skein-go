---
title: SQL snippets for analytics
author: ml
editors: sre
tags: sql, analytics, reference
created: 90
---
Queries we run often. All run against the analytics replica, never the primary.

Weekly active editors:

```sql
SELECT date_trunc('week', delivered_at) AS week, count(DISTINCT user_id) AS editors
FROM op_batches
WHERE delivered_at > now() - interval '90 days'
GROUP BY week
ORDER BY week;
```

Notes with the most rejected batches:

```sql
SELECT note_id, count(*) AS rejected
FROM rejected_batches
GROUP BY note_id
ORDER BY rejected DESC
LIMIT 10;
```

Agent runs that needed a confirmation:

```sql
SELECT run_id, tool, approved
FROM agent_confirmations
WHERE created_at > now() - interval '7 days';
```
