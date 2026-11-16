---
title: Tracing across Pub/Sub
author: sre
editors: storage
tags: observability, tracing, streaming
created: 165
---
A trace breaks at a message queue unless the producer writes the trace context into the message and the consumer reads it back.

1. The outbox relay copies `traceparent` from the outbox row into the message attributes.
2. The consumer starts a span linked to that context, not a child of it, because one message can be redelivered many times.
3. Redeliveries add a `messaging.redelivery_count` attribute.

```go
func inject(ctx context.Context, attrs map[string]string) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(attrs))
}
```

Back to [[Observability conventions]].
