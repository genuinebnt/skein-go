# Vertical slices

A slice ports one user-visible feature end to end: the route at the
gateway, the handler, the use case, the domain rules, the libraries
underneath, the storage, the tests and the build. Porting by slice instead
of by service means that after each one something works in Rust that a user
could do, and the e2e journeys for it pass.

The two ways combine: the per-service guides (`services/*/PORTING.md`) say
how to port every file of a service, and each slice picks the files a
feature needs, in the order to port them, across services and libraries.
Port the rest of a file's service later, from its guide.

| # | Slice | Services touched | e2e journeys that prove it |
|---|---|---|---|
| 1 | [Sign up and sign in](01-sign-up-and-sign-in.md) | identity (gateway optional) | `TestSignUpSignInAndSignOut`, `TestRefreshRotatesAndReuseRevokesTheSession` |
| 2 | [Create a note and edit it live](02-live-note-editing.md) | documents, identity, gateway | `TestTwoEditorsConvergeThroughConcurrentTypingAndAnOfflineSpell`, `TestAnEditSentJustBeforeClosingTheSocketIsKept`, `TestSeedNotesSyncToTheirTruthTextAndVersion` |
| 3 | [Search: text, typos, regex code search, titles](03-search.md) | search, documents (events), identity | `TestSearchFindsTextTypedIntoANoteOnceIndexed`, `TestRegexCodeSearchMatchesOnlyInsideCodeBlocks`, `TestTitleSuggestionsTolerateATypo`, `TestSeedSearchFindsEveryRelevantNote` |
| 4 | [The graph: links, backlinks, blast radius, paths](04-graph.md) | search, documents (events) | `TestBlastRadiusAndShortestPathFollowNewlyTypedLinks`, `TestSeedGraphMatchesTheTruth` |
| 5 | [Share and RBAC](05-share-and-rbac.md) | identity, gateway, documents, search | `TestWorkspaceMembersGetTheirRolesPermissions`, `TestShareLinksAreCreatedAndRevoked`, `TestAViewersEditIsRejectedAndNeverApplied`, `TestRemovingAMemberClosesTheirOpenSyncSocket`, `TestSeedPermissionMatrixHoldsThroughTheGateway` |
| 6 | [Notifications: a mention, live, in the inbox and by email](06-notifications.md) | notifications, documents (events), identity, gateway | `TestAMentionNotifiesLiveInTheInboxAndByEmail` |
| 7 | [One agent run](07-agent-run.md) | gateway, identity, documents, search, notifications; agent stays Python | `TestAgentSearchesAndCitesABlock`, `TestAgentDeletePausesForConfirmationAndDecliningKeepsTheNote` |

Order: 1, then 2, then any of 3 to 6, then 7. Slice 5 deepens what 1 and 2
started; 3 and 4 share the search service, so doing them back to back
saves setup.

## How to run a slice

1. Have the Go stack running natively (`make up-native`) and the e2e suite
   green against it. That is the baseline.
2. Port the slice's files in the order listed, each with its unit tests.
   Shared prerequisites (the libraries, `skein-proto`, the platform crates)
   are listed at the top of each slice; skip what an earlier slice already
   ported.
3. Build the Rust binary and swap it in for the Go one
   ([README.md, "Running Go and Rust side by side"](../README.md#running-go-and-rust-side-by-side)).
4. Run the slice's journeys (`make e2e RUN='TestSignUp|TestRefresh'`), then
   the whole suite: the RPCs the slice left stubbed must not be needed by
   anything else you run, so the suite also tells you what is still Go.

## Stubbing

A slice ports a service partially. The rule for what is not ported yet:

- **An RPC outside the slice returns `UNIMPLEMENTED`** from the Rust
  server. Do not fake behaviour; a journey that needs it should fail
  loudly.
- **A port the slice does not exercise gets a no-op or in-memory adapter**
  (a cache that always misses, an outbox that discards, a presence store in
  memory), named as such (`NoopCache`), and listed in the slice's "Stubbed"
  section, so it is clear what is still missing.
- **Another service stays in Go.** Cross-service calls go over the same
  protos, so a Rust documents works with Go identity and a Go gateway.

Each slice lists the Go files, protos, migrations, tests and stubs; the
per-service guides have each file's job and the Rust crates.
