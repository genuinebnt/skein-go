"""Agent evaluations: ``python -m skein_agent.evals``.

A fixture workspace (``fixture.py``) and a set of tasks (``tasks.py``). With
the scripted model the harness checks control flow (tools called, the
confirmation gate, permission refusals, injection handling, citations);
with ``--model anthropic`` and an API key it measures task success and
citation faithfulness on the real model.
"""
