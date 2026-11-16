"""Skein's agent service.

A LangGraph agent that acts as its user through the same gRPC APIs the
frontend uses (docs/adr/0009-agent-acts-as-the-user.md). Layout follows
ports and adapters (ADR-0013):

- ``domain``: pure rules and values (budgets, events, untrusted-content
  wrapping, the sequence CRDT used to write suggestions, edit planning). No
  I/O, no framework types.
- ``app``: use cases (runs, chats), the LangGraph graph, the tool catalogue,
  and the port interfaces they need.
- ``adapters``: gRPC server and clients, Anthropic model, Redis, Postgres
  stores and checkpointer (schema ``agent``), logging and OpenTelemetry.
- ``fakes``: in-memory implementations of every port, used by tests and the
  evals.
- ``evals``: ``python -m skein_agent.evals``.
"""
