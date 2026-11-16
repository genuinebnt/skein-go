"""In-memory implementations of every port, for tests and the evals.

``FakeWorkspace`` stands in for identity, documents, search, graph, ml and
notifications together, with the same permission model as ADR-0008 so tests
can show the agent being refused exactly where its user would be. Notes are
stored as real CRDT sequences, so suggestions written by the agent are
checked op for op.
"""
