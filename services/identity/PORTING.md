# Porting `services/identity` to Rust

Identity owns users, passwords, OAuth sign-in, sessions and refresh tokens,
workspaces and memberships, per-note role overrides, share links, client
replicas, and the one authorization function every other service calls
(`Authorize`). It signs every access token and publishes the public keys.
Read the overall plan in [docs/porting/README.md](../../docs/porting/README.md)
and the platform guide in [platform/PORTING.md](../../platform/PORTING.md)
first; slices [1 (sign up and sign in)](../../docs/porting/slices/01-sign-up-and-sign-in.md)
and [5 (share and RBAC)](../../docs/porting/slices/05-share-and-rbac.md) walk
through this service end to end.

Identity is the recommended first service to port: everything else depends
on it at run time, its access policy is a pure function pinned by every row
of the seed's permission matrix, and it exercises the whole stack (tonic,
sqlx, the outbox, a Redis cache, JWT signing) on simple data.

## Contract

### RPCs (`proto/skein/v1/identity.proto`, `docs/api/identity.md`)

| RPC | REST route | Auth | Use case |
|---|---|---|---|
| `SignUp` | `POST /v1/users` | public | `Service.SignUp` (`app/sessions.go`) |
| `SignIn` | `POST /v1/sessions` | public | `Service.SignIn` |
| `SignInWithOAuth` | `POST /v1/sessions:oauth` | public | `Service.SignInWithOAuth` (PKCE) |
| `Refresh` | `POST /v1/sessions:refresh` | public | `Service.Refresh` (rotation, reuse detection) |
| `SignOut` | `POST /v1/sessions:signOut` | public | `Service.SignOut` |
| `GetUser` | `GET /v1/users/{user_id}` | user | `Service.GetUser` (`app/workspaces.go`) |
| `CreateWorkspace` | `POST /v1/workspaces` | user | `Service.CreateWorkspace` |
| `ListMembers` | `GET /v1/workspaces/{workspace_id}/members` | user | `Service.ListMembers` |
| `SetWorkspaceRole` | `PUT /v1/workspaces/{workspace_id}/members/{user_id}` | user | `Service.SetWorkspaceRole` |
| `RemoveMember` | `DELETE /v1/workspaces/{workspace_id}/members/{user_id}` | user | `Service.RemoveMember` |
| `SetNoteRole` | `PUT /v1/notes/{note_id}/roles/{user_id}` | user | `Service.SetNoteRole` |
| `CreateShareLink` | `POST /v1/notes/{note_id}/share-links` | user | `Service.CreateShareLink` |
| `RevokeShareLink` | `DELETE /v1/share-links/{link_id}` | user | `Service.RevokeShareLink` |
| `RegisterReplica` | `POST /v1/replicas` | user | `Service.RegisterReplica` |
| `Authorize` | internal | service or user | `Service.Authorize` (`app/authorize.go`) |
| `ListReadableNotes` | internal | service or user | `Service.ListReadableNotes` |
| `IssueDelegatedToken` | internal | user | `Service.IssueDelegatedToken` (agent runs) |
| `RegisterNote` | internal | documents | `Service.RegisterNote` |

The five session RPCs are public (`PublicMethods()` in
`adapters/grpc/server.go`). Internal RPCs called without a user token are
refused unless `IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS` is set (dev
only, until service-to-service identity lands); with a token, a caller may
only ask about itself (`CheckInternalCaller`).

### Events

| Direction | Topic / subscription | Messages |
|---|---|---|
| publishes | `identity.events` (outbox) | `AccessChanged`, ordered by workspace ID |
| consumes | `identity.documents-events` on `documents.events` | `NoteCreated`, `NoteTrashed`, `NoteRestored`, `NotePurged` (the note projection: which workspace a note is in, who created it, whether it is trashed) |

### Storage

Schema `identity`, one migration `migrations/0001_identity.sql`: `users`,
`oauth_identities`, `sessions`, `refresh_tokens`, `workspaces`,
`memberships`, `notes` (the projection), `note_role_overrides`,
`share_links`, `replicas`, `one_time_tokens`, `outbox`, `processed_events`.
Redis: the `Authorize` decision cache (tagged by user, note and workspace)
and the sign-in rate limits.

### Admin HTTP (`IDENTITY_ADMIN_ADDR`)

`GET /.well-known/jwks.json` (every service fetches it), `/livez`,
`/readyz`, and, with `IDENTITY_DEV_SESSIONS=true` in dev and test only,
`POST` to the dev-session route the e2e suite uses to sign in seed users.

### Environment

`IDENTITY_GRPC_ADDR`, `IDENTITY_ADMIN_ADDR`, `IDENTITY_SIGNING_KEYS`
(secret; generated at startup in dev), `IDENTITY_ACCESS_TOKEN_TTL`,
`IDENTITY_REFRESH_TOKEN_TTL`, `IDENTITY_DELEGATED_TOKEN_MAX_TTL`,
`IDENTITY_SHARE_LINK_DEFAULT_TTL`, `IDENTITY_DECISION_CACHE_TTL`,
`IDENTITY_KEY_ROTATION_INTERVAL`, `IDENTITY_GOOGLE_CLIENT_ID` and `_SECRET`,
`IDENTITY_GITHUB_CLIENT_ID` and `_SECRET`, `IDENTITY_OAUTH_REDIRECT_URIS`,
`IDENTITY_DEV_SESSIONS`, `IDENTITY_FIXED_NOW` (tests),
`IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS`, plus the platform's.
Defaults and production checks are in `cmd/identity/settings.go`.

## Learn first

| Topic | Read | What to take from it |
|---|---|---|
| Argon2id | [RFC 9106](https://datatracker.ietf.org/doc/html/rfc9106), [OWASP password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html) | the parameters (m = 64 MiB, t = 3, p = 4 is RFC 9106's second recommendation), the PHC string, rehash on parameter change, why sign-in hashes a dummy for unknown emails |
| argon2 crate | [argon2](https://docs.rs/argon2/latest/argon2/), [subtle](https://docs.rs/subtle/latest/subtle/) | `Argon2::new(Algorithm::Argon2id, Version::V0x13, Params)`, `PasswordHash::new` parses the same PHC strings; constant-time comparison |
| JWT and JWKS | [RFC 8037](https://datatracker.ietf.org/doc/html/rfc8037), [RFC 7638](https://datatracker.ietf.org/doc/html/rfc7638), [RFC 9068](https://datatracker.ietf.org/doc/html/rfc9068) | what identity signs and publishes; key IDs as thumbprints; the rotation procedure in `internal/keys` |
| OAuth and PKCE | [RFC 7636](https://datatracker.ietf.org/doc/html/rfc7636), [OAuth 2.0 Security BCP (RFC 9700)](https://datatracker.ietf.org/doc/html/rfc9700), [oauth2 crate](https://docs.rs/oauth2/latest/oauth2/) | the code verifier rules `ValidCodeVerifier` checks; refresh token rotation and reuse detection (section 4.14 of the BCP), which `DecideRefresh` implements |
| RBAC | [NIST RBAC](https://csrc.nist.gov/projects/role-based-access-control) | roles, permissions and sessions; Skein is RBAC with per-object overrides |
| Zanzibar | [Zanzibar paper](https://research.google/pubs/zanzibar-googles-consistent-global-authorization-system/), [SpiceDB](https://github.com/authzed/spicedb) | relation tuples, check versus list ("lookup resources" is `ListReadableNotes`), and the "new enemy" problem that the revocation events and short cache TTL address; Skein does not need Zanzibar, but its vocabulary helps when the policy grows |
| outbox | [Transactional outbox](https://microservices.io/patterns/data/transactional-outbox.html) | why `AccessChanged` is written in the transaction that changes access |
| sqlx | [sqlx](https://docs.rs/sqlx/latest/sqlx/) | `Transaction`, `query_as`, `SELECT ... FOR UPDATE` for refresh token rotation |
| reference code | [launchbadge/realworld-axum-sqlx](https://github.com/launchbadge/realworld-axum-sqlx) | an axum and sqlx service with users, password hashing and tokens, written by the sqlx authors |

## Port order

Everything in `internal/domain`, `internal/credential` and `internal/keys`
goes into the `identity-core` crate with `internal/app` and `internal/fakes`.
Adapters and `cmd/` go into the `identity` crate.

### 1. Domain (`internal/domain`, pure)

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `ids.go` | `UserID`, `WorkspaceID`, `NoteID`, `ShareLinkID`, `SessionID`, `AgentRunID` as canonical lowercase UUID strings; `ReplicaID` as `u64`; `ParseUUID` rejects other spellings; `NewID` makes UUIDv7 | `uuid` | `TestParseUUIDCanonicalisesAndRejectsOtherSpellings` |
| `errors.go` | sentinel errors and `FieldError` (which field, why) | | used by every test |
| `roles.go` | `WorkspaceRole` (owner, admin, member, guest), `NoteRole` (viewer, commenter, editor), `Action` and the resource kind each action applies to; parse and print | | the policy tests |
| `session.go` | `User`, `Session`, `RefreshToken`, `DecideRefresh` (revoked, then reused, then expired), `OneTimeToken` and `DecideRedeem`, `Workspace`, `Member`, `AccessChange` and its cache tags | ids, roles | `TestDecideRefreshChecksRevocationThenReuseThenExpiry`, `TestOneTimeTokensAreSingleUseShortLivedAndPurposeBound`, `TestAccessChangeTagsCoverWhatItAffects` |
| `access.go` | `Membership`, `NoteRoleOverride`, `ShareLink`, `NoteAccess`, `Subject` (user or share link), `Check`, `Decision`, and `Facts` (the rows a batch of checks can touch) | roles, ids | the policy tests |
| `policy.go` | `Decide`, the single authorization function (rules 1 to 7 in `doc.go`), `Authorize` over a batch, `ReadableNotes`, `EffectiveNoteRole`; every decision names its rule | access | `TestDecideFollowsEveryRule`, `TestDecisionsAreConsistentAcrossEverySubjectActionAndResource`, `TestReadableNotesIsExactlyTheNotesDecideLetsYouRead`, **`TestDecideMatchesEverySeedPermissionMatrixRow`** (every row of `testdata/seed/small/truth/permissions/matrix.jsonl`, rule string included) |
| `rules.go` | `CheckRoleChange`, `CheckRemoval` (a workspace always keeps an owner), `CheckOverrideTarget`, `AccessLowered`, `NewShareLinkExpiry` (never editor), `NormalizeEmail`, `NormalizeName` (bounded in code points) | roles, access | `TestRoleChangesFollowTheOwnershipRules`, `TestRemovalKeepsAnOwnerAndNeedsAnOwnerToRemoveOne`, `TestShareLinksNeverGrantEditorAndDefaultToTheConfiguredLifetime`, `TestNormalizeEmail`, `TestNormalizeNameTrimsAndBoundsInCodePoints` |

Port `policy.go` with the seed matrix test before anything else in the
service. When it passes, the hardest correctness question in identity is
answered.

### 2. Credentials and keys (`internal/credential`, `internal/keys`)

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `credential/password.go` | Argon2id `Hasher` with a semaphore bounding concurrent hashes, PHC parse with explicit bounds, `Verify` reporting `NeedsRehash`, `VerifyDummy` | `argon2`, `subtle` | `TestVerifyAcceptsHashesFromTheReferenceImplementation` (hashes made by libsodium/argon2-cffi), `TestParserRejectsEverythingButCanonicalArgon2id`, `TestChangedParametersAskForARehashInEitherDirection`, `TestHashingWaitsForASlotAndHonoursCancellation`, `FuzzParsePasswordHash` (corpus in `credential/testdata/fuzz`) |
| `credential/strength.go` | the password strength policy | | `TestStrengthPolicy` |
| `credential/token.go` | opaque tokens: kind prefix (`rt_`, `sl_`, `ev_`, `pr_`) plus 32 random bytes base64url; `HashToken` is hex SHA-256 | `rand`, `base64`, `sha2` | `TestTokensHaveTheirKindsPrefixAndParseOnlyAsThatKind`, `TestHashTokenMatchesTheSeedScheme`, `FuzzParseToken` |
| `keys/keys.go` | `Keyring` of active, staged and retiring keys (invariants K1 to K4), `ParseConfig` for `IDENTITY_SIGNING_KEYS`, `Rotator`, `JWKS`, `Issue` | `skein-auth` | `TestRotationKeepsEveryUnexpiredTokenVerifiable`, `TestPublishedKeySetParsesAndKeyIDsAreThumbprints`, `TestStagingRules`, `TestParseConfig` |

### 3. Application and ports (`internal/app`)

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `ports.go` | `Store` with `InTx`, the `Tx` repositories (users, OAuth identities, sessions, workspaces, memberships, note roles, share links, note projection, replicas, one-time tokens), `Outbox`, OAuth provider, clock, token issuer, decision cache, rate limiter | domain | compiles; the fakes implement it |
| `service.go` | `Config`, `Deps`, `Service::new`; cache invalidation after commit; `RateLimitError` | ports | every app test |
| `authorize.go` | `Authorize` (batch, cached, capped), `ListReadableNotes`, `IssueDelegatedToken` (only for yourself, short, carries the run ID), `CheckInternalCaller`, `ApplyNoteEvent` (projection, once per event), `RegisterNote` | service, domain policy | `TestAuthorizeBatchesAndCapsTheBatchSize`, `TestListReadableNotesIsWhatAuthorizeAllows`, `TestInternalCallersMayOnlyAskAboutThemselves`, `TestDelegatedTokensCarryTheRunAreShortAndOnlyForYourself`, `TestNoteEventsUpdateTheProjectionOnceAndInvalidateDecisions`, `TestRegisteredNoteIsUsableAtOnceAndCannotBeMoved` |
| `sessions.go` | sign up, sign in (rate limited, dummy verify, rehash), OAuth (PKCE, account claiming), refresh (rotation under `FOR UPDATE`), sign out, email verification, password reset, dev sessions | service, credential, keys | all of `app/sessions_test.go`, notably `TestRefreshRotatesAndReuseRevokesTheWholeSession`, `TestConcurrentRefreshesOfOneTokenLetOnlyOneWin`, `TestSignInFailuresLookTheSame`, `TestOAuthClaimsAnUnverifiedAccountAndLocksThePreHijackerOut` |
| `workspaces.go` | users, workspaces, members, roles, note role overrides, share links, replicas; each change commits with its `AccessChanged` | service, domain rules | `TestCreatingAWorkspaceMakesTheCallerItsOwnerAndPublishesIt`, `TestAFailedCommitPublishesNothing`, `TestRemovingAMemberRevokesAccessAtOnceDespiteTheCache`, `TestNoteRolesLowerMembersGrantGuestsAndPublishWhatChanged`, `TestShareLinksAreViewerOrCommenterExpireAndRevoke`, `TestReplicasBelongToWhoeverRegisteredThemFirst` |

### 4. Fakes and the store contract

| Go file | Job | Proven by |
|---|---|---|
| `fakes/fakes.go` | clock, OAuth provider, token issuer | the app tests |
| `fakes/store.go` | in-memory `Store` with `CheckInvariants` and a record of published events | `TestFakeStoreMeetsTheStoreContract` |
| `storetest/storetest.go` | one contract suite run against the fake and against Postgres | the same suite in step 5 |

Port `storetest` as a generic function (`pub async fn run<S: Store>(make:
impl Fn() -> S)`) in `identity-core` behind a `testing` feature, so the
Postgres adapter's integration test can call it.

### 5. Adapters

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `migrations/0001_identity.sql`, `migrations/embed.go` | the schema | `skein-postgres` runner | `TestMigrationsApplyTwiceWithoutChange` |
| `adapters/postgres/store.go` | every `Tx` method as SQL; `InTx`; `outbox.Enqueue` for events | ports, migrations, `skein-outbox` | `TestPostgresStoreMeetsTheStoreContract`, `TestAccessChangesReachTheOutboxWithTheirCommit`, `TestSeedLoadsIntoPostgresAndAuthorizeMatchesTheMatrix` |
| `adapters/redis/cache.go` | the decision cache over `redisx.Cache` with tag invalidation | `skein-redis` | the app tests through the port; `TestRemovingAMemberRevokesAccessAtOnceDespiteTheCache` |
| `adapters/events/events.go` | encode `AccessChanged` envelopes; decode note events | `skein-proto` | `TestAccessChangedCarriesTheChangeOrderedByWorkspace`, `FuzzDecodeNoteEvent` |
| `adapters/pubsub/consumer.go` | `documents.events` to `ApplyNoteEvent`, idempotently | events, `skein-events` | `TestDocumentsEventsMaintainTheProjectionExactlyOnce`, `TestMalformedEventsAreRetriedThenDeadLettered` |
| `adapters/oauth/oauth.go` | Google and GitHub code exchange with PKCE, userinfo, primary email | `reqwest` | `TestGoogleExchangesWithPKCEAndReadsUserinfo`, `TestGitHubUsesTheNumericIDAndThePrimaryEmail` (use `wiremock` in place of `httptest`) |
| `adapters/grpc/errors.go` | app and domain errors to `Status` with `BadRequest`, `ErrorInfo`, `PreconditionFailure`, `RetryInfo`, `RequestInfo` | `tonic-types` | `TestErrorsCarryTheDocumentedDetails`, `TestRateLimitBecomesResourceExhaustedWithRetryInfo` |
| `adapters/grpc/server.go` | the tonic service: map requests, take the caller from verified claims, call the use case | errors, app | `TestTokensFromSignUpVerifyAgainstThePublishedKeySet`, `TestWorkspaceFlowOverGRPC`, `TestDelegatedTokenActsForTheUserWithTheRunID`, `TestTokenlessGetUserAndListMembersNeedTheInternalCallFlag` |
| `adapters/admin/admin.go` | the admin router: JWKS, health, dev sessions | keys, app | `TestDevSessionRouteDoesNotExistUnlessEnabled`, `TestDevSessionRouteAnswersLikeSignIn` |

### 6. Seed loader and main

| Go file | Job | Proven by |
|---|---|---|
| `internal/seed/seed.go`, `cmd/identity-seed/main.go` | load identity's share of `testdata/seed/<profile>` through the store, idempotently; refuse a tampered dataset (manifest checksums) | `TestLoadingTheSmallSeedLoadsIdentitysShare`, `TestLoadingTwiceChangesNothing`, `TestLoaderRefusesATamperedOrForeignDataset`, `TestAuthorizeAfterLoadingMatchesEveryMatrixRow`, `TestListReadableNotesAgreesWithEveryMatrixReadRow` |
| `cmd/identity/settings.go` | read and check every variable; production refuses dev-only settings and generated keys | `TestDefaultsInDevNeedNoSecrets`, `TestProductionRefusesUnsafeSettings`, `TestSigningKeysComeFromTheSecretAndAreNeverEchoed`, `TestDevSessionsAreRefusedOutsideDevAndTest` |
| `cmd/identity/main.go` | wiring in the order of `docs/platform.md`: config, telemetry, logging, Postgres and migrations, Redis, Pub/Sub, outbox relay, consumer, gRPC server, admin server, shutdown | the e2e suite |

## Dependencies

- Libraries: none.
- Platform: `config`, `logging`, `telemetry`, `auth` (signing), `ratelimit`,
  `redisx`, `postgres`, `events`, `outbox`, `grpcx`, `httpx`, `shutdown`.
- Generated code: `skein-proto` (identity, events, common).
- Other services: none at run time. Documents' events keep its note
  projection current; until documents runs, `RegisterNote` (which
  documents calls on create) keeps new notes usable at once.

## Crates

`tonic`, `tonic-types`, `prost`, `sqlx`, `redis`, `argon2`, `subtle`,
`jsonwebtoken`, `ed25519-dalek`, `rand` (`OsRng`), `base64`, `sha2`, `uuid`
(v7), `chrono`, `reqwest` (OAuth), `serde`/`serde_json`, `thiserror`,
`tracing`; tests: `proptest`, `wiremock`, `tokio::test`.

## Pitfalls

- **Argon2 blocks the runtime.** Each hash takes 60 to 150 ms of CPU and 64
  MiB. Run it in `tokio::task::spawn_blocking` behind a `Semaphore` (the Go
  `MaxConcurrent`), and keep the dummy verification on the same path so
  timing does not reveal unknown emails.
- **The `argon2` crate's parser bounds differ from Go's.** Keep the explicit
  bounds checks from `password.go` before handing the string to the crate;
  a corrupted row must not make verification allocate gigabytes.
- **Base64 strictness.** Go's strict decoders reject non-canonical trailing
  bits; use `base64`'s `URL_SAFE_NO_PAD` and `STANDARD_NO_PAD` engines,
  which do the same, and keep the fuzz targets.
- **Refresh token races.** Rotation reads the token `FOR UPDATE` inside the
  transaction; `TestConcurrentRefreshesOfOneTokenLetOnlyOneWin` fails if the
  lock is lost in translation.
- **Rule strings are part of the contract.** Decisions carry the rule name
  spelled as in the seed matrix (`workspace_role:member`,
  `share_link:expired`); a typo fails the matrix test with the rule named.
- **Optional times.** Go uses `*time.Time`; Rust uses
  `Option<DateTime<Utc>>`. Truncate to microseconds before comparing with
  stored values.
- **Cache staleness.** Invalidation runs after commit; a crash in between
  leaves stale decisions for at most `IDENTITY_DECISION_CACHE_TTL`. Do not
  "fix" this by invalidating inside the transaction: a rollback would then
  have dropped valid entries and a concurrent reader could re-cache the old
  decision before the commit.
- **Dev-only routes.** The dev-session route and unauthenticated internal
  calls must stay impossible outside dev and test; port the settings tests
  that refuse them.

## Done when

- every Go test above passes in Rust under the same name (snake case);
- the seed matrix test passes in the domain and after loading the seed into
  Postgres;
- tokens signed by Rust identity verify in Go services and the reverse;
- `make e2e` passes with Rust identity and every other service in Go,
  notably `TestSignUpSignInAndSignOut`,
  `TestRefreshRotatesAndReuseRevokesTheSession`,
  `TestWorkspaceMembersGetTheirRolesPermissions`,
  `TestShareLinksAreCreatedAndRevoked`,
  `TestSeedPermissionMatrixHoldsThroughTheGateway` and
  `TestSeedRemovedMembersSeeNothing`.
