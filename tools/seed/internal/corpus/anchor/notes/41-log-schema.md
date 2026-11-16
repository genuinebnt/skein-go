---
title: Log schema
author: sre
editors: admin
tags: observability, logging, conventions
created: 140
---
Logs are JSON, one object per line, written to stdout. These fields are required:

| Field | Type | Example |
| --- | --- | --- |
| ts | RFC 3339 with milliseconds | 2026-10-02T09:14:03.120Z |
| level | debug, info, warn, error | warn |
| service | string | documents |
| msg | short, constant string | apply rejected |
| trace_id | 32 hex characters | 4bf92f3577b34da6a3ce929d0e0e4736 |
| request_id | string | req_01JB8Y2M3K |

Never log note text, comment bodies, tokens or email addresses. Log IDs instead, and look the content up with access checks when debugging.

```go
logger.Warn("apply rejected",
	"note_id", noteID,
	"replica_id", replicaID,
	"reason", "access_revoked",
)
```

Back to [[Observability conventions]].
