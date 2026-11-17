# Skein web

The browser client: a Vite + React 19 + strict TypeScript app that edits
notes offline through the same CRDT the services use (compiled to
WebAssembly from `libs/crdt` and `libs/blocks`), talks to the gateway over
the REST API and three WebSockets, and draws the screens from the design
boards in the genuine.dev visual system.

It runs in two modes:

| Mode | What answers the API | Use it for |
|---|---|---|
| **mock** (`pnpm dev`) | An in-process fake gateway over `testdata/seed/small`, serving the same REST shapes, errors, headers and WebSocket frames | UI work, demos, tests, no backend needed |
| **live** (`pnpm dev:live`, `pnpm build`) | The gateway at `VITE_API_BASE`, or this origin with the dev server proxying `/v1` | Running against `make up` or a deployment |

## Commands

From the repository root:

```sh
make web-dev      # mock mode on http://localhost:5173 (WEB_PORT=… to change)
make web-test     # bridge Go tests, typecheck, lint, Vitest, Playwright e2e
make web-build    # live-mode build into web/dist, WebAssembly included
```

Or in `web/` with pnpm (Node 22+, pnpm 10+):

| Command | Does |
|---|---|
| `pnpm install` | Install dependencies |
| `pnpm dev` | Mock mode dev server. Builds the mock seed and the WebAssembly module first if missing |
| `pnpm dev:live` | Live mode dev server; proxies `/v1` (and its WebSockets) to `VITE_GATEWAY`, default `http://localhost:8080` |
| `pnpm build` | Typecheck and build live mode into `dist/` (no mock code or seed data is included) |
| `pnpm build:mock` / `pnpm preview:mock` | A static mock-mode build in `dist-mock/`, and a preview server for it |
| `pnpm typecheck` | `tsc -b` over the app, tests, Vite and Playwright config |
| `pnpm lint` | ESLint with zero warnings allowed (typescript-eslint, react-hooks, jsx-a11y) |
| `pnpm test` | Vitest: unit, integration (against the mock gateway) and component tests |
| `pnpm e2e` | Playwright in mock mode, desktop and a phone viewport. Starts its own dev server on `E2E_PORT` (default 25174) |
| `pnpm wasm` | Rebuild `public/wasm/` from `web/wasm` (TinyGo if installed, else Go; `SKEIN_WASM_COMPILER=go\|tinygo` to choose) |
| `pnpm gen:api` | Regenerate `src/api/schema.gen.ts` from `gen/openapi/skein.swagger.json`; `pnpm gen:api --check` fails if it is stale |
| `pnpm gen:seed` | Rebuild `.mock-seed/` from `testdata/seed/small` |

Playwright needs its browser once: `pnpm exec playwright install chromium`.

## Configuration

All optional; set them in `web/.env.local` or the environment.

| Variable | Mode | Meaning |
|---|---|---|
| `VITE_API_BASE` | live | Gateway origin, e.g. `https://skein.example.com`. Unset: same origin. If it is another origin the gateway must allow it (CORS) |
| `VITE_GATEWAY` | live dev | Where `pnpm dev:live` proxies `/v1` when `VITE_API_BASE` is unset |
| `VITE_WORKSPACE_ID` | live | Workspace to open: the API cannot list a user's workspaces yet |
| `VITE_OAUTH_GITHUB_CLIENT_ID`, `VITE_OAUTH_GOOGLE_CLIENT_ID` | live | Enable the OAuth buttons (authorization code + PKCE, redirect to `/auth/callback`) |

In mock mode, sign in as any seed user with any password. The **simulate**
menu in the header takes the network offline, expires the access token, or
revokes your edit access, to show offline, refresh and rejected-batch flows.

## How it fits together

```
src/
  api/        generated types and operation table; ApiClient (errors, retries,
              idempotency keys, rate-limit headers, pagination, token refresh)
  ws/         ReconnectingSocket (close codes, backoff, heartbeat, online/offline),
              the sync protobuf codec, agent and notification channels
  crdt/       WebAssembly loader and typed document; UTF-16 ↔ code point index;
              IndexedDB op store; sync reducer; NoteSession (one open note)
  editor/     block editor: Markdown shown around the cursor, slash menu, blocks
  agent/      agent run model and panel (steps, citations, suggestions, confirms)
  search/     palette: text, meaning and code (regex, lang:)
  graph/      layout and interaction for the graph explorer
  history/    diff and blame models
  screens/    one file per screen
  mocks/      the fake gateway (mock mode and tests only; never in a live build)
  styles/     genuine.dev's folio.css and tokens, plus Skein's own parts
wasm/         Go module compiled to WebAssembly: a handle-based bridge over libs/crdt
              and libs/blocks (ADR-0007)
e2e/          Playwright specs
```

Things worth knowing before changing them:

- **Positions.** The CRDT counts Unicode code points (ADR-0003); the DOM
  counts UTF-16 units. `crdt/utf16.ts` converts at the bridge, nowhere else.
- **Offline.** Every local batch goes into IndexedDB together with the
  replica's counter in one transaction (ADR-0004); a counter that does not
  match on open means another tab or a restore, and the replica is rotated.
  Unconfirmed batches are resent on reconnect (ops are idempotent).
- **No ack frame.** The sync protocol has none, so a batch counts as
  delivered once the socket stays open for `ACK_GRACE_MS` after sending.
- **Rejected batches** become a draft the user can reinsert or discard; the
  local copy is rebuilt from the server under a new replica ID.
- **Block attributes** (type, indent, checked, language) are last-writer-wins
  registers that the sync contract cannot carry yet. In live mode they are
  recovered from the server's Markdown; mock mode sends them as a proposed
  `attrs` frame.
- **Agent edits.** The agent applies `apply_edit` immediately as its own
  replica; the panel shows each edit as a suggestion: accept keeps it,
  reject writes the inverse edit as the user.
- **Overflow.** Every `overflow: auto` region has `overscroll-behavior:
  contain`, and flex scroll children `min-height: 0`.

## Live API coverage

Screens call the generated operations where the contract has them. Where it
does not, the UI is built as designed and marked **needs backend**; mock
mode serves the proposed route so the screen works end to end.

| Screen | Live operations | Proposed or missing |
|---|---|---|
| Sign in, OAuth callback | `SignIn`, `SignInWithOAuth`, `Refresh`, `SignOut` | |
| Editor | `GetNote`, `RenameNote`, `ListVersions`, `RegisterReplica`, `Undo`, `Redo`, sync WebSocket | block attributes in sync, ack frame, `tags` on notes |
| Comments | | all comment routes |
| Agent panel | `CreateChat`, `ListChats`, `GetRun`, `CancelRun`, `Confirm`, agent WebSocket | messages in `ListChats`, the agent's replica ID |
| History | `ListVersions`, `Diff`, `Blame`, `GetTextAt`, `CreateNote` | `NameVersion` |
| Search palette | `Search`, `SuggestTitles`, `SemanticSearch` | snippet on semantic hits |
| Discover, clusters | `Discover`, `ListClusters`, `BenchmarkClustering` | |
| Graph, reading plan | `GetLinkGraph`, `AnalyzeGraph`, `BlastRadius`, `ShortestPath`, `ReadingPath`, `ReadingPlan` | the candidates left out of a plan |
| Share dialog | `SetNoteRole`, `CreateShareLink`, `RevokeShareLink` | list a note's roles and links, invite by email |
| Members | `ListMembers`, `SetWorkspaceRole`, `RemoveMember` | admin policy APIs |
| Inbox | `ListInbox`, `MarkRead`, `Get/UpdatePreferences`, notifications WebSocket | |
| Workspace picker | | `GET /v1/workspaces` (falls back to `VITE_WORKSPACE_ID`) |
