# Slice 6: notifications (a mention, live, in the inbox and by email)

A user types `@ada` into a block of a note Ada can read. Ada's open
notifications socket receives the new entry at once, her inbox lists it,
and, because mentions email by default, an email arrives after the digest
delay unless she has read it in the meantime. A mention of someone who
cannot read the note sends nothing; ten edits mentioning the same person
within the window notify once.

## The path

```
documents (slice 2)
  debounce -> BlocksChanged{note_id, blocks[{block_id, kind, language, text}], removed_block_ids}
  outbox -> documents.events
notifications
  adapters/pubsub/consumers.go (notifications.documents-events) -> Decode -> dispatch
  app/events.go Producer.BlocksChanged
    domain.ExtractHandles per block (code skipped)      (domain/mentions.go)
    domain.NewlyMentioned vs block_mentions projection  (domain/rules.go)
    domain.MentionCandidates -> Candidates with NeedsRead
  app/producer.go Producer.apply
    phase 1, no transaction: Directory.ResolveHandles (identity GetUser/ListMembers),
                             Authorizer.Authorize READ per recipient (identity, cached)
    phase 2, one transaction: MarkProcessed; SetBlockMentions; for each candidate:
             drop (unreadable, or both channels off) | refresh same source |
             coalesce into an unread one within the window |
             insert + EnqueueEmail (if email is on for MENTION)
    after commit: UnreadCache.Invalidate, Hub/Fanout publish, wake EmailSender
  live:  Fanout (Redis pub/sub between replicas) -> Hub -> Subscription
         -> grpc Subscribe stream -> gateway wsapi serveNotifications -> WebSocket
  email: EmailSender: plan (domain.PlanEmails, digest per recipient after the delay)
         -> claim (lease) -> email.Renderer.Render -> email.Encode -> smtp.Mailer.Send
         -> sent | retry with backoff | dead
browser GET /v1/notifications -> Inbox.ListInbox
```

## Contracts and storage

- Protos: `notifications.proto` (`NotificationService`, `Notification`,
  `NotificationKind`, preferences), `events.proto` (`BlocksChanged`,
  `ChangedBlock`, `NotificationRequested`), `identity.proto` (`Authorize`,
  `GetUser`, `ListMembers`).
- Migration: `services/notifications/migrations/0001_init.sql`
  (`notifications`, `preferences`, `notes`, `block_mentions`,
  `email_jobs`, `email_sends`, `processed_events`).
- Fixtures: `testdata/conformance/notifications/mentions.json` (the mention
  grammar), `email_plans.json` (the digest planner); the seed's expected
  inbox `testdata/seed/small/notifications.jsonl`.
- WebSocket framing: `docs/api/websocket.md`, "NotificationService.Subscribe".

## Files to port, in order

Prerequisites: slices 1 and 2 (or Go identity and documents), plus
`platform/redisx` and `platform/events`; Mailpit runs in the native stack.

Notifications ([services/notifications/PORTING.md](../../../services/notifications/PORTING.md)):

| # | Go file | What this slice needs |
|---|---|---|
| 1 | `internal/domain/errors.go`, `kind.go` | all |
| 2 | `internal/domain/notification.go` | all |
| 3 | `internal/domain/preferences.go` | all |
| 4 | `internal/domain/mentions.go` | `ExtractHandles`, `Excerpt` |
| 5 | `internal/domain/rules.go` | `MentionedUser`, `MentionedBlock`, `BlockMentions`, `MentionCandidates`, `NewlyMentioned` (the other rules can wait) |
| 6 | `internal/domain/email.go` | `PlanEmails`, `RetryDelay` |
| 7 | `internal/domain/page.go` | the inbox cursor |
| 8 | `internal/email/email.go` | rendering and encoding |
| 9 | `internal/app/ports.go` | all |
| 10 | `internal/app/producer.go` | `apply` and its helpers |
| 11 | `internal/app/events.go` | `BlocksChanged`, the note events (titles for the email), `NotePurged` |
| 12 | `internal/app/inbox.go` | `ListInbox`, `UnreadCount`, `MarkRead`, preferences |
| 13 | `internal/app/live.go` | `Hub`, `Subscription` |
| 14 | `internal/app/emailsender.go` | all |
| 15 | `internal/fakes/store.go`, `services.go` | all |
| 16 | `internal/adapters/postgres/store.go`, `email.go` | all |
| 17 | `internal/adapters/identity/identity.go` | `Authorize`, `Profile`, `ResolveHandles`, `InvalidateUser` |
| 18 | `internal/adapters/redis/redis.go` | `UnreadCache`, `Fanout` |
| 19 | `internal/adapters/smtp/smtp.go` | `Mailer` |
| 20 | `internal/adapters/pubsub/consumers.go` | the documents subscription (the other three can follow) |
| 21 | `internal/adapters/grpc/server.go` | all five RPCs |
| 22 | `cmd/notifications/main.go`, `admin.go` | wiring |

Gateway, if Rust: `internal/adapters/wsapi/handlers.go` (`serveNotifications`)
and the four REST routes.

## Tests that prove it

Conformance: `TestMentionGrammarMatchesConformanceFixtures`,
`TestEmailPlansMatchConformanceFixtures`.

Domain and email: `TestTruncateKeepsCodePointLimitAndMarksTheCut`,
`TestBuildTruncatesTimesToMicrosecondsInUTC`,
`TestDefaultPreferencesMatchTheDocumentedDefaults`,
`TestHTMLPartEscapesUserText`, `TestSubjectCannotInjectHeaders`,
`TestEncodedMessageParsesAsMultipartAlternativeWithStableMessageID`.

App: `TestMentioningSomeoneWhoCannotReadTheNoteSendsNothing`,
`TestRedeliveredEventChangesNothing`,
`TestAFailedLookupLeavesTheEventUnprocessedForRetry`,
`TestTenEditsMentioningTheSamePersonNotifyOnce`,
`TestAMentionRetypedWithinTheWindowCoalescesAndAfterItNotifiesAgain`,
`TestMentionsInCodeOfUnknownHandlesAndOfTheActorAreIgnored`,
`TestRemovedBlocksForgetTheirMentions`,
`TestDefaultPreferencesEmailMentionsButNotComments`,
`TestPagingThroughTheInboxReturnsEveryItemOnceNewestFirst`,
`TestSubscribersOnlyReceiveTheirOwnNotifications`,
`TestASlowSubscriberIsDroppedInsteadOfBlockingOrSkipping`,
`TestNothingIsSentBeforeTheDigestDelay`,
`TestANotificationReadBeforeItsEmailIsNotEmailed`,
`TestEmailOutboxNeverLosesAndDuplicatesOnlyAfterACrashBetweenSendAndMark`.

Adapters: `TestHandlesResolveFromEmailLocalPartsAcrossPages`,
`TestFanoutReachesOnlyReplicasThatJoinedTheUser`,
`TestRepliesAreClassifiedForRetry`,
`TestSubscribeStreamsTheCallersNewNotifications`,
`TestEventsOnEveryTopicReachTheInbox`; integration
`TestConcurrentSendersNeverClaimTheSameSend`.

Seed: `TestProducerRulesReproduceTheSeedInbox`.

End to end: `TestAMentionNotifiesLiveInTheInboxAndByEmail`
(`e2e/notify_test.go`; it waits for the one-minute digest delay and reads
Mailpit), and `TestEverySubscriptionBacksOffBeforeDeadLettering`
(`e2e/infra_test.go`) for the subscription settings.

## Stubbed in this slice

- Comments, access changes (slice 5), agent events (slice 7) and
  `NotificationRequested`: their consumers ack and do nothing.
- `UpdatePreferences` can wait if the journey uses the defaults
  (mentions are emailed by default), but port it before the web app uses it.

## Build

```
cd rust
cargo test -p notifications-core
cargo build -p notifications && cargo test -p notifications --features integration
```
