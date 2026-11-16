# 0008. Users, workspace and note roles, server-side checks

Date: 2026-11-16. Status: accepted.

## Decision
- Workspace roles: owner, admin, member, guest. Note roles: editor,
  commenter, viewer, inherited from the workspace with per-note overrides and
  share links (role, expiry, revocable, stored as hashes).
- One check, `Authorize(subject, action, resource)`, implemented by
  `identity` and called (with a short cache) by every service. Actions are an
  enum in the proto, not strings.
- `documents` checks every incoming batch of operations, not only the
  connection. A CRDT merges anything it receives, so the server is the only
  place a viewer's edit can be refused.
- Passwords use Argon2id. Sessions are short-lived signed access tokens plus
  rotating refresh tokens. Removing access publishes an event that closes the
  user's open sync streams.

## Consequences
- Search, ML and the agent must filter by permission before returning
  results (ADR-0009, ADR-0010). Permission data reaches them as events.
