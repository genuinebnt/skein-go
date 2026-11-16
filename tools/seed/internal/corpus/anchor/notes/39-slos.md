---
title: SLOs and burn-rate alerts
author: sre
editors: admin
tags: observability, slo, alerting
created: 155
---
Our user-facing SLOs, measured over 30 days.

| SLO | Target | Measured at |
| --- | --- | --- |
| Sync availability | 99.9% | gateway WebSocket accept and first ack |
| Search latency | 95% under 300 ms | gateway |
| Agent first token | 90% under 2 s | gateway |

## Alerts

- Fast burn: page when the error budget burns at 14.4 times the sustainable rate over 1 hour (2 percent of the monthly budget gone).
- Slow burn: ticket when it burns at 6 times over 6 hours.

```yaml
- alert: SyncAvailabilityFastBurn
  expr: (1 - sli:sync_availability:ratio_rate1h) > (14.4 * 0.001)
  for: 2m
  labels:
    severity: page
```

Back to [[Observability conventions]].
