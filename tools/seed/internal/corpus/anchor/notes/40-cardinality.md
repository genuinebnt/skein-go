---
title: Metric cardinality budget
author: sre
editors: admin
tags: observability, metrics
created: 145
---
Each service may export at most 20,000 active time series. CI counts the series a service exposes after a load test and fails the build above the budget.

- Labels must come from a small, fixed set: method, status class, route template.
- Route templates, never raw paths: `/notes/{id}`, not `/notes/0192...`.
- High-cardinality detail belongs in traces and exemplars.

In September the search service briefly exported 310,000 series because a label carried the raw query string. The bill noticed before we did. Back to [[Observability conventions]].
