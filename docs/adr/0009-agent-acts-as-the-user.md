# 0009. The agent acts as the user, through the same APIs

Date: 2026-11-16. Status: accepted.

## Context
The agent should be able to do anything its user can (search, read, create,
edit, organise, share) and grow with new features without bespoke code.

## Decision
- The agent is a LangGraph graph in the `agent` service. Its tools are thin
  wrappers over the same gRPC APIs the frontend uses; a new API becomes a new
  tool, not a new agent feature.
- Each run gets a delegated token from `identity` that identifies the user,
  the agent and the run, with the user's permissions and nothing more. Every
  service checks it like any other token.
- Retrieval is filtered by permission before the model sees anything. Note
  text is treated as data, never as instructions.
- Edits are applied as a CRDT peer with its own replica ID, so they show as
  the agent's in history and can be undone as one group. In the editor they
  appear as suggestions the user can accept or reject.
- Destructive or outward-facing actions (delete a note, change sharing,
  invite someone, send email) pause the graph with an interrupt and require
  explicit confirmation from the user.
- Every run stores its trace (messages, tool calls, results, costs). Runs
  have step, token, time and cost budgets.

## Consequences
- The agent can never exceed its user's permissions, by construction.
- Tool schemas are generated from the proto files to keep them in sync.
