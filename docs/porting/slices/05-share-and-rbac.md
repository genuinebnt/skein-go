# Slice 5: share and RBAC

An owner creates a workspace, adds members with roles, lowers one member to
viewer on a single note, creates and revokes share links, and removes a
member. Every service enforces the result at once: a viewer's edit is
refused, a removed member's open sync socket is closed, search stops
returning what they can no longer read, and the seed's permission matrix
holds through the gateway. This slice ports identity's access model in full
and the `AccessChanged` reaction in every service that caches permissions.

## The model

Roles (ADR-0008, `services/identity/internal/domain/doc.go`):

- workspace roles: owner, admin, member, guest;
- note roles: viewer, commenter, editor, set per note per user as an
  override that can lower a member or grant a guest, never raise a member;
- share links: viewer or commenter on one note, with an expiry, revocable;
  never editor (refused on creation and clamped when read);
- actions: `READ`, `COMMENT`, `EDIT`, `DELETE`, `SHARE` on notes;
  `NOTE_CREATE`, `AGENT_RUN`, `MANAGE_MEMBERS`, `MANAGE_SETTINGS` on
  workspaces; an agent run is decided exactly as its user.

`domain.Decide` is the one function that answers every check, and it names
the rule that decided (`workspace_role:member`, `note_override:viewer`,
`share_link:expired`, ...), spelled as in the seed matrix.

## The path of a change

```
browser PUT /v1/notes/{noteId}/roles/{userId} {role: VIEWER}
gateway -> IdentityService.SetNoteRole (idempotency key accepted)
identity
  app/workspaces.go SetNoteRole
    actorRole + requireNoteShare (the caller needs SHARE)
    domain.CheckOverrideTarget, domain.AccessLowered
    Store.InTx: upsert note_role_overrides + EnqueueAccessChanged (outbox)
    after commit: decision cache InvalidateTags(user, note, workspace)
  outbox relay -> identity.events (ordering key = workspace ID)
consumers of AccessChanged:
  gateway        identityevents -> app.Revocations.Apply (local) + RevocationBus (Redis)
                 -> app.Registry.Revoke: close the affected sockets (4403/4404)
  documents      pubsub/accesschanged -> Service.AccessChanged
                 -> AuthCache.InvalidateUser + recheckSessions (reject, then close)
  search         Projector.HandleIdentity -> invalidate readable sets and pages by tag
  notifications  Producer.AccessChanged -> SHARE (guest) or ACCESS_CHANGED (member)

every later request: Authorize(subject, action, resource) -> domain.Decide
```

## Contracts and storage

- Protos: `identity.proto` (`CreateWorkspace`, `ListMembers`,
  `SetWorkspaceRole`, `RemoveMember`, `SetNoteRole`, `CreateShareLink`,
  `RevokeShareLink`, `Authorize`, `ListReadableNotes`, `WorkspaceRole`,
  `NoteRole`, `Action`), `events.proto` (`AccessChanged`).
- Migration: identity's `0001_identity.sql` (`workspaces`, `memberships`,
  `notes`, `note_role_overrides`, `share_links`, `outbox`,
  `processed_events`).
- Ground truth: `testdata/seed/small/truth/permissions/matrix.jsonl` (6,256
  rows: subject, action, resource, time, allowed, rule).

## Files to port, in order

Prerequisites: slice 1 (identity's base, platform) and slice 2 (documents
and the gateway sync bridge, if they are Rust), plus `platform/outbox` and
`platform/redisx`.

Identity ([services/identity/PORTING.md](../../../services/identity/PORTING.md)):

| # | Go file | What this slice needs |
|---|---|---|
| 1 | `internal/domain/roles.go` | all |
| 2 | `internal/domain/access.go` | all (`Facts`, `Check`, `Decision`, subjects) |
| 3 | `internal/domain/policy.go` | `Decide`, `Authorize`, `ReadableNotes`, `EffectiveNoteRole` |
| 4 | `internal/domain/rules.go` | `CheckRoleChange`, `CheckRemoval`, `CheckOverrideTarget`, `AccessLowered`, `NewShareLinkExpiry` |
| 5 | `internal/domain/session.go` | `Workspace`, `Member`, `AccessChange` and its cache tags |
| 6 | `internal/app/ports.go` | workspace, membership, note role, share link, note projection repositories; `Outbox`; decision cache |
| 7 | `internal/app/authorize.go` | `Authorize`, `ListReadableNotes`, `CheckInternalCaller`, `ApplyNoteEvent`, `RegisterNote` |
| 8 | `internal/app/workspaces.go` | `CreateWorkspace`, `ListMembers`, `SetWorkspaceRole`, `RemoveMember`, `SetNoteRole`, `CreateShareLink`, `RevokeShareLink`, `ResolveShareLink` |
| 9 | `internal/app/service.go` | `invalidate`, `invalidateChanges` |
| 10 | `internal/adapters/postgres/store.go` | the workspace, membership, override, share link and note projection methods; `EnqueueAccessChanged` |
| 11 | `internal/adapters/events/events.go` | `AccessChanged` encoding, `DecodeNoteEvent` |
| 12 | `internal/adapters/pubsub/consumer.go` | `documents.events` to the note projection |
| 13 | `internal/adapters/redis/cache.go` | the decision cache |
| 14 | `internal/adapters/grpc/server.go` | the workspace and access methods, `internalSubject` |
| 15 | `internal/seed/seed.go`, `cmd/identity-seed/main.go` | the seed loader |

Gateway ([services/gateway/PORTING.md](../../../services/gateway/PORTING.md)),
if Rust: `internal/domain/revocation.go`; `internal/app/revocations.go`,
`sockets.go` (`Revoke`); `internal/adapters/identityevents/identityevents.go`;
`internal/adapters/redisstore/revocations.go`.

Documents ([services/documents/PORTING.md](../../../services/documents/PORTING.md)),
if Rust: `internal/adapters/eventcodec/codec.go` (`DecodeAccessChanged`),
`internal/adapters/pubsub/accesschanged.go`, `internal/app/lifecycle.go`
(`AccessChanged`, `recheckSessions`), `internal/adapters/redis/redis.go`
(`AuthCache.InvalidateUser`).

Search, if Rust: `internal/app/projector.go` (`HandleIdentity`,
`invalidate`, `UserTag`, `WorkspaceTag`), `internal/app/access.go`.

Notifications, if Rust: `internal/app/events.go` (`AccessChanged`),
`internal/domain/rules.go` (`AccessCandidates`) (see [slice 6](06-notifications.md)).

## Tests that prove it

Identity unit: `TestDecideFollowsEveryRule`,
`TestDecisionsAreConsistentAcrossEverySubjectActionAndResource`,
`TestReadableNotesIsExactlyTheNotesDecideLetsYouRead`,
**`TestDecideMatchesEverySeedPermissionMatrixRow`**,
`TestRoleChangesFollowTheOwnershipRules`,
`TestRemovalKeepsAnOwnerAndNeedsAnOwnerToRemoveOne`,
`TestOverridesApplyOnlyToMembersAndGuests`,
`TestShareLinksNeverGrantEditorAndDefaultToTheConfiguredLifetime`,
`TestCreatingAWorkspaceMakesTheCallerItsOwnerAndPublishesIt`,
`TestAFailedCommitPublishesNothing`,
`TestRemovingAMemberRevokesAccessAtOnceDespiteTheCache`,
`TestNoteRolesLowerMembersGrantGuestsAndPublishWhatChanged`,
`TestShareLinksAreViewerOrCommenterExpireAndRevoke`,
`TestAuthorizeBatchesAndCapsTheBatchSize`,
`TestInternalCallersMayOnlyAskAboutThemselves`.

Identity seed and integration: `TestAuthorizeAfterLoadingMatchesEveryMatrixRow`,
`TestListReadableNotesAgreesWithEveryMatrixReadRow`,
`TestSeedLoadsIntoPostgresAndAuthorizeMatchesTheMatrix`,
`TestAccessChangesReachTheOutboxWithTheirCommit`,
`TestDocumentsEventsMaintainTheProjectionExactlyOnce`.

Consumers: gateway `TestRevokeClosesOnlyTheAffectedSockets`,
`TestRevocationsCloseLocallyEvenWhenTheBroadcastFails`,
`TestOnlyRevocationsAreApplied`; documents
`TestRevokedAccessMidSessionRejectsBatchesThenClosesTheStream`,
`TestAccessChangedReachesTheUseCaseAndOtherMessagesAreAcked`; search
`TestAccessChangedDropsCachedPermissions`.

End to end: `TestWorkspaceMembersGetTheirRolesPermissions`
(`workspace_test.go`), `TestShareLinksAreCreatedAndRevoked` (`api_test.go`),
`TestAViewersEditIsRejectedAndNeverApplied`,
`TestRemovingAMemberClosesTheirOpenSyncSocket` (`sync_test.go`),
`TestSeedPermissionMatrixHoldsThroughTheGateway`,
`TestSeedRemovedMembersSeeNothing` (`seed_test.go`).

## Stubbed or missing

- **Share links cannot be redeemed yet.** `CreateShareLink` and
  `RevokeShareLink` work and `Decide` handles link subjects, but
  `ResolveShareLink` (turning a presented `sl_` token into a subject) has no
  RPC or gateway route; only identity's unit tests call it. Port it, and add
  the route through the contracts first if you need it.
- Service-to-service identity: internal RPCs are trusted on the caller's
  user token, with `IDENTITY_ALLOW_UNAUTHENTICATED_INTERNAL_CALLS` for
  tokenless calls in dev. Keep the flag's behaviour; the roadmap replaces it.
- Redis pub/sub for revocations is at most once: a replica that misses a
  broadcast leaves a socket open, and documents' per-batch check is the
  backstop. Do not remove that check in the port.

## Build

```
cd rust
cargo test -p identity-core          # includes the 6,256-row matrix test
cargo test -p identity --features integration
```
