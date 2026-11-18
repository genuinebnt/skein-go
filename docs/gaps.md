# Known gaps

Every gap found by the sweep on 2026-11-17: the follow-ups and decisions in
every merged pull request (#1 to #31), every service README's follow-ups,
`TODO`/`FIXME` markers (none in first-party code), and the docs
(architecture, api, observability, platform, seed-data, local-native,
testing, porting) checked against the code for ports, environment
variables, metric names, routes and make targets.

Status is one of **fixed here** (in the pull request that added this file),
**fixed earlier** (verified on `main`), or **deferred** (with the reason, and
a plan where one is clear). The owner area is where the fix belongs, not who
found it. Update a row when its gap closes; add rows as gaps are found.

## Deferred, most important first

| # | Gap | Found by | Why deferred / plan | Owner area |
|---|---|---|---|---|
| D1 | **ml checks no permissions.** `SemanticSearch`, `Discover`, `ListClusters` and `BenchmarkClustering` answer for any workspace or note, whoever asks; ADR-0010 says ml filters by permission during the HNSW descent. The gateway only rate-limits these routes. Reproduced on the native stack: a newly signed-up user who belongs to no workspace gets 0 notes from `ListNotes` on the anchor workspace, but 10 semantic hits (note and block IDs) and all 6 clusters (61 note IDs, with top terms drawn from note text) from ml. | porting docs (#30), confirmed here | Not small: ml has no caller identity, no identity client and per-workspace (not per-user) caches. See [the plan below](#plan-for-d1-permission-aware-ml). | ml, with identity, gateway, deploy |
| D2 | **Share links cannot be redeemed.** They can be created and revoked, but `ResolveShareLink` (identity's app layer) has no caller: no RPC or route takes a link token, and `Subject` has no share-link field. | porting docs, identity (#20) | Contract change: a `RedeemShareLink` RPC, or a share-link field on `Subject` that `Authorize` resolves; then a gateway route. The policy's link branch is ready. | contracts, identity, gateway |
| D3 | **Nothing publishes to `agent.events`.** notifications subscribes (`notifications.agent-events`, `AGENT_RUN_FINISHED`) and `pubsub-init.sh` creates the topic, but the agent never publishes, so those notifications only come from the seed. | porting docs | The agent has Postgres: add an outbox table and relay (as the Go services do through `platform/outbox`) publishing `NotificationRequested` when a run finishes or pauses for confirmation. Python event adapters belong to local-first in batch 3. | agent |
| D4 | **libs/dp exports with no production caller**: `ChunkBlocks`/`CheckChunks`, `Segmenter`, `LCS`/`LCSLength` (also `Diff`, `DiffLines`, `DiffWords`, `Levenshtein`). Only tests call them. | porting docs | Decide per function: wire `ChunkBlocks` into ml's embedding input or the agent's context packing, `Segmenter` into search's CJK tokenisation; or mark them library-only so the Rust port can schedule them last. | libs/dp, search, ml |
| D5 | **Agent metrics do not match the contract.** The code emits `agent.runs`, `agent.run.steps`, `agent.tokens`, `agent.cost`, `agent.tool.*`, `agent.rate_limited` (no `skein.` prefix). `docs/observability.md` and the Grafana agent dashboard use `skein.agent.run.duration`, `.runs.active`, `.step.duration`, `.budget.exhaustions`, `.confirmation.*`, so the dashboard is empty. | this sweep | Not a rename: three documented instruments do not exist. Implement the documented set in `skein_agent/observability.py` with the documented buckets, then drop the extras or document them. | agent |
| D6 | **Outbox metrics do not match the contract.** `platform/outbox` emits `skein.outbox.published`, `.publish_failures` and `.publish_lag`, and `docs/platform.md` describes those; `docs/observability.md` and the dashboards use `skein.outbox.pending`, `.lag` (gauges) and `.relay.publish.duration`. | this sweep | Add the two observable gauges (a count and the oldest unpublished row's age, read by the relay) and rename the histogram, or change the dashboards; then make the two docs agree. | platform (outbox), observability |
| D7 | **ml and the agent are missing from the RED dashboards.** ml hand-rolls its RPC metrics (`skein.ml.rpc.duration` in ms, labelled `rpc.grpc.status_code`) instead of `rpc.server.call.duration` in seconds, which also breaks the seconds rule; the agent records no RPC server metrics at all. | this sweep | Use `opentelemetry-instrumentation-grpc` with the v1.45.0 conventions, as `docs/observability.md` says. | ml, agent |
| D8 | **Backend response headers never reach REST clients.** The gateway's outgoing header matcher drops them all (on purpose), so `x-skein-run-usage` (agent) and `skein-graph-version`, `-metrics-version`, `-metrics-pending` (search) are visible to gRPC callers only. | this sweep (READMEs corrected) | Contract fields instead of headers: `RunUsage` in `GetRunResponse` (proposed in #16) and version fields on `AnalyzeGraphResponse` (proposed in #15). | contracts, agent, search |
| D9 | **No `ListWorkspaces` RPC.** The web client calls a "proposed" `GET /v1/workspaces`; live mode always falls back to `VITE_WORKSPACE_ID`. | this sweep | Add the RPC to identity (the caller's memberships, paginated). | contracts, identity, web |
| D10 | **Service-to-service identity.** `IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS` opens four identity RPCs to token-less callers; `IssueDelegatedToken` cannot verify that the agent is calling. | #20, #26 | On the roadmap: mTLS or service tokens in `platform/`, then remove the switch. | platform, identity |
| D11 | **Docker stack loads half the seed.** `make seed-load` runs only identity-seed and documents-seed; compose has no search-seed or notifications-seed service (`make up-native` runs all four). | this sweep (docs/seed-data.md corrected) | Add the two seed services to `deploy/compose/services/` and the Makefile target. | deploy/compose |
| D12 | **`deploy/local/ports.env`'s convention comment** ("291NN, NN the last two digits of the compose port") does not hold for search (compose 9093, native 29103) or notifications (19095, 29104). | this sweep | Reword the comment; owned by devx in batch 3. | deploy/local |
| D13 | **No per-service binary override in `up.sh`** (for example `SKEIN_NATIVE_IDENTITY_BIN`) to swap in a Rust build. | #30 | Owned by devx. | deploy/local |
| D14 | **Open contract proposals**: `CommentAdded` event (comment notifications only come from the seed); `NOTIFICATION_KIND_AGENT_CONFIRMATION_NEEDED`; `User.handle`; `AccessChanged.role`; `ChangedBlock.position`; `NoteCreated.is_root` or `NoteRootChanged`; ml `NoteSimilarities`; an identity key-set RPC (HTTP JWKS is used); `GET /v1/users/me`; email verification and password reset RPCs (use cases exist); a sync ack frame; block attributes on the wire; an agent suggestion flag; an email RPC in notifications; `ClusteringResult.implementation` should list `scratch-kmeans++`. | #3, #6, #14, #15, #16, #20, #21, #26 | Each is a contract change for the owner of `proto/`; the proposals are in those PRs and the service READMEs. | contracts |
| D15 | **platform/httpx gaps**: `WriteError` and `Chain`'s recovery write a non-`google.rpc.Status` shape; `CORS` has no exposed-header list; `AccessLog` takes no route override; `RateLimit` supports one key. The gateway carries its own versions. | #12 | Move the gateway's `edge` versions into platform. | platform |
| D16 | **CRDT**: no restore op (undoing a delete reinserts copies), no element iterator (`blocks.Elements` replays twice), no op lookup by ID (reused-ID batches are not atomic); recursive `CheckInvariants` forces TinyGo's 1 MiB stack. | #21, #23, #29 | Library work in `libs/crdt`, then simplify documents and web. | libs/crdt |
| D17 | identity accepts replica 2 (documents' server replica) and other IDs below 2^32; only 0 is refused. Documents refuses client batches from replica 2 itself. | #21 | Refuse in identity's `ParseReplicaID`, after checking the seed's replica IDs. | identity |
| D18 | No periodic cleanup of expired refresh and one-time tokens (the index exists). | #20 | A ticker in identity's main or a scheduled job. | identity |
| D19 | No backfill or republish RPC for full projection rebuilds; search's existing projections need a rebuild after the link-title fix in #26. | #15, #26 | A documents RPC that replays a workspace's events. | documents, search |
| D20 | **CI does not run**: the web Playwright suite (`pnpm e2e`, part of `make web-test`); Docker image builds; the testcontainers path of the integration tests (CI uses the native overrides). | this PR | Kept out of scope: the jobs added here cover what was asked. Playwright needs `playwright install --with-deps chromium` and would suit `e2e.yml`; image builds suit a separate workflow. | CI |
| D21 | ml: one service-wide lock (per-workspace locks next); HNSW graphs rebuilt on startup instead of persisted; no Kubernetes manifest. | #5, ml README | Performance and deploy work; not needed at current sizes. | ml, deploy |
| D22 | Graph betweenness and diameter too slow per request at 10k notes (cached per version today); whole-note character diffs cost about 450 B per code point. | #2, #4 | Sampled Brandes and iFUB; line-then-word diffs in documents. | libs/graph, libs/dp, documents |
| D23 | Cross-replica `CancelRun` cannot reach a run working on another agent replica (it stops at its deadline). | #16 | Needs fan-out (Redis channel) or run ownership routing. | agent |
| D24 | Gateway: sign-in routes share the per-IP `api` budget (no brute-force budget); workspace-level revocations do not close the notifications socket. | #12 | A `signin` budget; a workspace lookup for notification sockets. | gateway |
| D25 | Observability: Jaeger pinned to 2.20 for Grafana's v1 API; no alerting rules; no CORS on the collector's OTLP HTTP receiver for browser telemetry. | #11 | Bump when Grafana's Jaeger source uses API v3; add rules now that services exist. | observability |
| D26 | Cross-language crosscheck corpora (`testdata/crosscheck/`, `make crosscheck`) do not exist yet. | #29, porting docs | Planned with the first Rust crate. | libs (porting) |
| D27 | Seed: no graph diameter, betweenness or PageRank truth; `large` is about 6.6 GB until run-length encoding (wire v2). | #9 | Generator work. | tools/seed |
| D28 | One unexplained Vitest failure seen once in #23, never reproduced (not in this PR's CI runs either). | #23 | Watch the `web` job. | web |

### Plan for D1: permission-aware ml

1. **Contain now (gateway).** Before forwarding `SemanticSearch`,
   `ListClusters` and `BenchmarkClustering`, call identity's `Authorize`
   with the caller's token for read access to the workspace, and for
   `Discover` read access to the note (the gateway already pre-checks
   `ACTION_NOTE_READ` before a sync upgrade, so the pattern exists). Answer
   `NOT_FOUND` as the other routes do. This stops outsiders, not members
   whose access to particular notes is lowered.
2. **Filter properly (ml).** Add a `Permissions` port to `skein_ml.app`
   (`readable_notes(token, workspace_id) -> frozenset[str]`) with a gRPC
   adapter calling `IdentityService.ListReadableNotes` with the forwarded
   bearer token, an in-memory fake for tests, and `SKEIN_ML_IDENTITY_ADDR`
   wiring in compose and `deploy/local/up.sh`. The gRPC adapter passes the
   token from `authorization` metadata into the use cases.
   - `semantic_search`: intersect the readable set with the tag filter
     before the descent (the index already takes an allowed set, which is
     what ADR-0010 asks for).
   - `discover`: `NOT_FOUND` if the note is unreadable; drop unreadable
     neighbours before ranking.
   - `list_clusters`: cluster only the readable notes, so labels and top
     terms never come from notes the caller cannot read.
   - Caches: key search results and clusters by a hash of the readable set
     as well as the workspace (users with the same access share entries),
     still tagged `workspace:<id>` for invalidation.
   - search's calls (`TopicRelevance`, `NoteCosines`) forward the user's
     token already through `platform/grpcx`; check that they keep working.
3. **Tests**: use-case tests with the fake for each RPC (an outsider gets
   nothing, a member with a lowered note sees everything else, cluster terms
   exclude unreadable text); an e2e journey reproducing the probe above.

## Fixed here

| # | Gap | Found by | Fix | Owner area |
|---|---|---|---|---|
| F1 | CI ran no Python tests, no web checks, no seed determinism check and no integration tests. | #5, #8, #9, #14, #23, #26, #28 | `ci.yml` jobs `ml` (pip, with the bench extra so the oracle tests run, plus `-m loopback`), `agent` (`uv sync --frozen`), `ruff` (pinned 0.17.5, check and format), `web` (bridge tests, wasm build, typecheck, lint, Vitest, build), `seed` (regenerate small and diff, `make seed-check`), `integration` (`make up-native` and `make integration-native` on the runner). Go modules, pip, uv and pnpm are cached. | CI |
| F2 | No end-to-end workflow; `make ci` does not vet `e2e/`. | #26, #28 | `e2e.yml`: `make up-native` and `make e2e`, nightly, on demand (with a `-run` input) and on pull requests that change `e2e/`, `deploy/local/` or the workflow. `make e2e` runs gofmt and vet on the suite first. | CI |
| F3 | `ubuntu-latest` is ubuntu-26.04, which has no PostgreSQL 16. | this PR | The shared `.github/actions/native-stack` installs it from the PostgreSQL apt repository when missing. | CI |
| F4 | `TestAViewersEditIsRejectedAndNeverApplied` still expected a 4403 close after #28 made a demoted viewer's socket stay open; it failed after 90 s. | this PR | The test now asserts the socket stays open and the edit comes back `rejected`. | e2e |
| F5 | ml failed `ruff check` on current ruff (B008 on three frozen-dataclass defaults). | this PR | `extend-immutable-calls` in `services/ml/pyproject.toml`. | ml |
| F6 | `docs/testing.md` named a nonexistent `make test-service`. | this sweep | Replaced with the real per-service command. | docs |
| F7 | `services/search/README.md` pointed at identity on `localhost:9091`; compose publishes 9101. | this sweep | Corrected. | search |
| F8 | `services/documents/README.md` omitted the `SKEIN_TEST_POSTGRES_DSN` path its integration test supports. | this sweep | Added. | documents |
| F9 | `docs/seed-data.md` said loaders "come later"; `docs/architecture.md` said every service, ml and agent included, has a seed loader. | this sweep | Both now name the four loaders and how ml gets its projection; architecture §8 mentions CI and links this file. | docs |
| F10 | `docs/api/agent.md` and `websocket.md` examples called a `search_notes` tool (the tool is `search`) with a `changedSince` argument `SearchRequest` does not have. | #16 | Corrected. | docs/api |
| F11 | The agent README said `x-skein-run-usage` reaches REST clients as `Grpc-Metadata-X-Skein-Run-Usage`; search's README implied its graph version headers do. The gateway forwards no backend header. | this sweep | Both READMEs say gRPC callers only (the contract fix is D8). | agent, search |
| F12 | gateway, documents, notifications and ml metrics were missing from `docs/observability.md` (documents' and ml's were documented nowhere). | #12, this sweep | A "Service metrics" table from the code, pointing here for D5 to D7. | observability |
| F13 | `docs/gaps.md` did not exist. | #28 | This file, linked from the README and architecture §8. | docs |

## Fixed earlier (verified on main)

| Gap | Found by | Fixed in |
|---|---|---|
| Demoting a member to viewer closed their sync socket with 4403 | #26 | #28 (re-check on AccessChanged) |
| e-means vs FAISS test failed for about 5% of seeds | #16 | #28 (shared assignment kernel) |
| Share-link 7-day default contradicted the proto and docs | #20 | #28 |
| Internal RPCs and messages listed in the public OpenAPI | #3 | #28 (visibility rules) |
| `ML_ADDR` empty in compose's search, so semantic path weights were off | #26 | #28 |
| Gateway had no integration-tagged tests | #12, #26 | #28 (real Redis) |
| `BlastRadiusRequest` had no direction | #3, #4 | `direction` field is in `search.proto` |
| No identity key set for verifiers | #8, #12, #15 | identity serves `/.well-known/jwks.json` |
| ml used private generated stubs (`_gen/`) | #5 | ml uses `gen/python` |
| `libs/dp` and `libs/trigram` missing from `WASM_LIBS` | #2, #15 | Makefile |
| `tools/seed` tests not in `make test` | #9 | `tools` is in `GO_MODULES` |
| Agent README said a disconnect cancels a run; websocket.md said it does not | #12 | agent README now agrees with websocket.md |
| Services not wired into compose | #12 to #21 | #22 |
| Port-order table: trigram depends on dp | #29 | #31 |
