# Porting `services/notifications` to Rust

Notifications turns facts other services publish (a mention typed into a
block, an access change, a finished agent run, an explicit request) into
inbox entries, live pushes and email, according to each user's
preferences. It owns the inbox, the per-kind preferences, a projection of
note titles and block mentions, and an email outbox with digests, retries
and dead letters.

Read [docs/porting/README.md](../../docs/porting/README.md) and
[platform/PORTING.md](../../platform/PORTING.md) first. Slice
[6 (notifications)](../../docs/porting/slices/06-notifications.md) walks
through a mention end to end. The service README (`README.md` here) has the
configuration table.

## Contract

### RPCs (`proto/skein/v1/notifications.proto`, `docs/api/notifications.md`)

| RPC | REST route | Use case (`internal/app`) |
|---|---|---|
| `ListInbox` | `GET /v1/notifications` | `Inbox.ListInbox` (newest first, cursor pages, unread filter) |
| `MarkRead` | `POST /v1/notifications:markRead` | `Inbox.MarkRead` (idempotent, own notifications only) |
| `Subscribe` | WebSocket `GET /v1/notifications/live` through the gateway | `Hub.Subscribe` (`live.go`) |
| `GetPreferences` | `GET /v1/notification-preferences` | `Inbox.Preferences` |
| `UpdatePreferences` | `PUT /v1/notification-preferences` | `Inbox.UpdatePreferences` |

### Events consumed

| Subscription | Payload | Effect |
|---|---|---|
| `notifications.requests` | `NotificationRequested` | `Producer.Request` |
| `notifications.agent-events` | `NotificationRequested` from an agent run | `Producer.AgentEvent` (only to the run's own user) |
| `notifications.documents-events` | `NoteCreated`, `NoteRenamed`, `NoteTrashed`, `NoteRestored`; `NotePurged`; `BlocksChanged` | the note projection; erase on purge; `MENTION` for newly mentioned users |
| `notifications.identity-events` | `AccessChanged` | `SHARE` or `ACCESS_CHANGED`, after dropping cached authorization answers |

Comments have no event payload yet; the seed loader drives
`Producer.Comment` directly. Notifications publishes nothing; it does not
use the platform relay, but the email outbox is the same idea for SMTP.

### Calls to other services

Identity: `Authorize` (can the recipient read the note at the time of the
fact; cached and invalidated by `AccessChanged`), `GetUser` and
`ListMembers` for display names and handles (`ResolveHandles` maps
`@handle` to users through email local parts, across pages).

### Storage

Schema `notifications`, `migrations/0001_init.sql`: `notifications`,
`preferences`, `notes` and `block_mentions` (projections), `email_jobs`
and `email_sends` (the email outbox), `processed_events`. Redis: unread
counts (`NOTIFICATIONS_UNREAD_CACHE_TTL`) and the live fan-out channel
between replicas.

### Environment

`GRPC_ADDR`, `HTTP_ADDR` (admin), `IDENTITY_GRPC_TARGET`,
`IDENTITY_JWKS_URL`, `GRPC_CLIENT_INSECURE`, `APP_BASE_URL` (links in
email), `SMTP_ADDR`, `SMTP_FROM`, `SMTP_HELO_NAME`, `SMTP_USERNAME`,
`SMTP_PASSWORD`, `SMTP_REQUIRE_TLS`, `NOTIFICATIONS_COALESCE_WINDOW`,
`NOTIFICATIONS_DIGEST_DELAY`, `NOTIFICATIONS_EMAIL_POLL_INTERVAL`,
`NOTIFICATIONS_UNREAD_CACHE_TTL`, plus the platform's.

## Learn first

| Topic | Read | What to take from it |
|---|---|---|
| idempotent consumers | [Idempotent consumer](https://microservices.io/patterns/communication-style/idempotent-consumer.html), [Idempotent receiver (Fowler)](https://martinfowler.com/articles/patterns-of-distributed-systems/idempotent-receiver.html) | one processed-event row per consumer and event ID, in the effect's transaction |
| Pub/Sub failure handling | [Handling message failures](https://cloud.google.com/pubsub/docs/handling-failures) | redelivery backoff, dead-letter topics, `delivery_attempt` |
| outbox for side effects | [Transactional outbox](https://microservices.io/patterns/data/transactional-outbox.html) | the email outbox applies the same pattern to SMTP: write the job with the notification, send later |
| SKIP LOCKED leases | [SELECT ... FOR UPDATE SKIP LOCKED](https://www.postgresql.org/docs/current/sql-select.html#SQL-FOR-UPDATE-SHARE) | how concurrent senders claim sends without blocking each other |
| email format | [RFC 5322](https://datatracker.ietf.org/doc/html/rfc5322), [RFC 3834](https://datatracker.ietf.org/doc/html/rfc3834) | `Message-ID` (stable across a re-send so clients fold duplicates), header injection, `Auto-Submitted: auto-generated` |
| lettre | [lettre](https://docs.rs/lettre/latest/lettre/) | the message builder (multipart/alternative, quoted-printable) and the async SMTP transport with STARTTLS |
| templates | [askama](https://docs.rs/askama/latest/askama/), [minijinja](https://docs.rs/minijinja/latest/minijinja/) | compile-time (askama) or run-time (minijinja) templates with HTML auto-escaping, the equivalent of `html/template` |
| live fan-out | [Redis pub/sub](https://redis.io/docs/latest/develop/interact/pubsub/) | at-most-once delivery between replicas; why a client reconciles with `ListInbox` |
| backpressure | [I'm not feeling the async pressure](https://lucumr.pocoo.org/2020/1/1/async-pressure/) | why a slow subscriber is dropped and told to resubscribe |

## Port order

Domain, email rendering, app, ports and fakes go into
`notifications-core`; adapters and `cmd/` into `notifications`.

### 1. Domain (`internal/domain`, pure)

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `errors.go` | sentinel errors | | |
| `kind.go` | `Kind` with the proto's numbers, `ProtoName`, `ParseKind` | | `TestKindProtoNamesRoundTrip` |
| `notification.go` | `Location`, `Source` (what caused it), `Notification` with `Validate`, `Candidate.Build` (truncates times to microseconds in UTC), `Truncate` in code points, UUID helpers | kind | `TestUUIDRoundTripsThroughBytes`, `TestTruncateKeepsCodePointLimitAndMarksTheCut`, `TestValidateRejectsBrokenNotifications`, `TestBuildTruncatesTimesToMicrosecondsInUTC` |
| `preferences.go` | per-kind inbox and email switches, defaults, updates; `CheckInvariants` | kind | `TestDefaultPreferencesMatchTheDocumentedDefaults`, `TestPreferenceUpdateReplacesOnlyListedKinds`, `TestPreferenceUpdateRejectsDuplicateAndUnknownKinds`, `TestStoredPreferencesWithUnknownKindsAreIgnored` |
| `mentions.go` | `ExtractHandles` (the @mention grammar, over code points, skipping code), `Excerpt` | | **`TestMentionGrammarMatchesConformanceFixtures`** (`testdata/conformance/notifications/mentions.json`) |
| `rules.go` | the producer rules: `CommentCandidates`, `AccessCandidates`, `AgentCandidates`, `RequestCandidates`, `MentionCandidates`, `NewlyMentioned` | notification, mentions | `TestCommentNotifiesMentionedReadersCreatorAndParentAuthorOnce`, `TestEveryCommentByOthersNotifiesTheNotesCreator`, `TestAccessRulesDistinguishSharesFromChangesAndRemovals`, `TestAgentRunsNotifyTheirUserAndConfirmationsHaveTheirOwnSource` |
| `email.go` | `PlanEmails` (one email or a digest per recipient), `EmailPlan.CheckInvariants`, `RetryDelay` | notification | **`TestEmailPlansMatchConformanceFixtures`** (`email_plans.json`) |
| `page.go` | `PageSize`, `InboxCursor` encode and decode | | the inbox tests |

The two conformance tests are the cross-language contract for this
service; port them first.

### 2. Email rendering (`internal/email`)

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `email/email.go` | `Renderer`: plain and HTML parts from embedded templates, links under `APP_BASE_URL`, one notification or a digest; `Encode` to an RFC 5322 multipart message with a `Message-ID` derived from the send ID and a boundary derived from the `Message-ID` | domain | `TestHTMLPartEscapesUserText`, `TestSubjectCannotInjectHeaders`, `TestEncodedMessageParsesAsMultipartAlternativeWithStableMessageID`, `TestDigestListsEveryNotificationAndLinksTheInbox`, `TestNewRendererNeedsAnAbsoluteHTTPURL` |

### 3. Application and ports (`internal/app`)

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `ports.go` | `Store` with `WithTx`, the `Tx` facets (inbox, projection, email), `Authorizer`, `Directory`, `Mailer`, `Fanout`, `UnreadCache`, `Clock`, `IDGenerator` | domain | the fakes implement it |
| `producer.go` | `Producer.apply` in two phases: lookups outside any transaction, then one transaction that records the event, updates projections and places candidates (drop, refresh same source, coalesce within the window, or insert and enqueue email); after commit, invalidate unread counts, publish live, wake the sender | ports, rules | `TestRedeliveredEventChangesNothing`, `TestAFailedLookupLeavesTheEventUnprocessedForRetry`, `TestTenEditsMentioningTheSamePersonNotifyOnce`, `TestAMentionRetypedWithinTheWindowCoalescesAndAfterItNotifiesAgain`, `TestANewerFactFromTheSameSourceRefreshesTheNotification`, `TestPreferencesDecideInboxAndEmail` |
| `events.go` | one method per event kind (`Request`, `AgentEvent`, `Comment`, `AccessChanged`, the note events, `BlocksChanged`) | producer | `TestCommentNotifiesTheNotesCreatorAndMentionedReaders`, `TestMentioningSomeoneWhoCannotReadTheNoteSendsNothing`, `TestMentionsInCodeOfUnknownHandlesAndOfTheActorAreIgnored`, `TestRemovedBlocksForgetTheirMentions`, `TestAccessChangeForAGuestIsAShareAndForAMemberAChange` |
| `inbox.go` | `ListInbox`, `UnreadCount`, `MarkRead`, preferences | ports | `TestPagingThroughTheInboxReturnsEveryItemOnceNewestFirst`, `TestNotificationsCreatedWhilePagingDoNotShiftLaterPages`, `TestAPageTokenReusedWithAnotherFilterIsRejected`, `TestMarkReadOnlyTouchesTheCallersNotificationsAndIsIdempotent` |
| `live.go` | `Hub`: subscribers per user, joins the fan-out on the first and leaves after the last, drops a slow subscriber; `CheckInvariants` | ports | `TestHubJoinsOnTheFirstSubscriberAndLeavesAfterTheLast`, `TestSubscribersOnlyReceiveTheirOwnNotifications`, `TestASlowSubscriberIsDroppedInsteadOfBlockingOrSkipping` |
| `emailsender.go` | `EmailSender`: plan, claim (lease), send, record sent, retry with backoff and jitter, or dead; the duplicate window is only between SMTP accepting and the "sent" mark | ports, email, domain email | `TestABurstBecomesOneDigestPerRecipient`, `TestANotificationReadBeforeItsEmailIsNotEmailed`, `TestPermanentFailuresAndExhaustedRetriesAreDeadLettered`, `TestALateSuccessIsRecordedButALateFailureIsNot`, **`TestEmailOutboxNeverLosesAndDuplicatesOnlyAfterACrashBetweenSendAndMark`** (randomised crash injection) |

### 4. Fakes (`internal/fakes`)

`store.go` (in-memory store with failing commits and `CheckInvariants`) and
`services.go` (authorizer, directory, mailer, fan-out bus across fake
replicas, unread cache).

### 5. Adapters

| Go file | Job | Needs first | Proven by |
|---|---|---|---|
| `migrations/0001_init.sql` | the schema | `skein-postgres` | applies on the native Postgres |
| `adapters/postgres/store.go`, `email.go` | the store, inbox queries with the cursor, projection writes, email job and send leases (`FOR UPDATE SKIP LOCKED`), `FinishEmailSend` checking the attempt number | ports | `TestPostgresStoreMatchesTheFakeOnAProducerScenario`, `TestConcurrentSendersNeverClaimTheSameSend`, `TestSeedReplayOnPostgresReproducesTheExpectedInbox` |
| `adapters/identity/identity.go` | `Authorizer` and `Directory` over identity, with caches; handles from email local parts | `skein-grpc` | `TestActionNumbersMatchTheProto`, `TestAuthorizeCachesAnswersAndAsksOnlyForMisses`, `TestAccessChangesDropTheUsersCachedAnswers`, `TestHandlesResolveFromEmailLocalPartsAcrossPages` |
| `adapters/redis/redis.go` | `UnreadCache` (fails open), `Fanout` over Redis pub/sub with `EncodeLive`/`DecodeLive` | `skein-redis` | `TestUnreadCountFailsOpenWhenRedisIsDown`, `TestFanoutReachesOnlyReplicasThatJoinedTheUser`, `TestLiveMessagesRoundTrip` |
| `adapters/smtp/smtp.go` | `Mailer` over SMTP with deadlines; classify replies as transient or permanent; optional required TLS | `lettre` | `TestRepliesAreClassifiedForRetry`, `TestUnreachableServerIsTransient`, `TestAStalledServerIsCutOffAtTheDeadline`, `TestRequireTLSRefusesAPlaintextServer` |
| `adapters/pubsub/consumers.go` | the four subscriptions, decode, dispatch, ack or nack | app, `skein-events` | `TestEventsOnEveryTopicReachTheInbox`, `TestARepublishedEventIsAcknowledgedAsADuplicate`, `TestUnprocessableMessagesEndInTheDeadLetters` |
| `adapters/grpc/server.go` | the tonic service; `Subscribe` as a server stream; a slow subscriber is told to resubscribe | app | `TestEveryRPCNeedsAnAuthenticatedCaller`, `TestSubscribeStreamsTheCallersNewNotifications`, `TestASlowSubscriberIsToldToResubscribe` |

### 6. Seed and main

| Go file | Job | Proven by |
|---|---|---|
| `internal/seed/*.go`, `cmd/notifications-seed/main.go` | replay the seed's facts through the producer and compare with `notifications.jsonl`; load the expected inbox directly | `TestProducerRulesReproduceTheSeedInbox`, `TestReplayingTwiceIsAllDuplicates`, `TestLoadingTheInboxKeepsIDsAndIsIdempotent`, `TestUnreadCountsOfTheLoadedInboxMatchTheSeed` |
| `cmd/notifications/main.go`, `admin.go` | wiring: consumers, hub and fan-out, email sender, gRPC and admin servers | the e2e suite |

## Dependencies

- Libraries: none (the mention grammar is in `domain/mentions.go`).
- Platform: `config`, `logging`, `telemetry`, `auth`, `redisx`, `postgres`,
  `events`, `outbox` (processed events), `grpcx`, `shutdown`.
- Generated code: `skein-proto` (notifications, events, identity client).
- Other services: identity; events from documents, identity and the agent;
  an SMTP server (Mailpit locally).

## Crates

`tonic`, `prost`, `sqlx`, `redis` (pub/sub needs a dedicated connection),
`lettre` (tokio, rustls, builder), `askama` or `minijinja`, `uuid`,
`chrono`, `rand` (jitter), `thiserror`, `tracing`; tests: `proptest`,
`tokio::test` with paused time for the digest delay and coalescing window.

## Pitfalls

- **No network inside a transaction.** Phase 1 of the producer looks things
  up; phase 2 writes. In Rust it is easy to call an `async` client while
  holding a `Transaction`; do not.
- **Best-effort after commit.** Cache invalidation, live push and waking the
  sender may fail without failing the event. Do not propagate their errors
  into the consumer (that would redeliver and double-count).
- **Microseconds.** Notification times are truncated to microseconds when
  built, so cursors round-trip through Postgres exactly.
- **Deterministic email bytes.** The MIME boundary is derived from the
  `Message-ID`, so encoded output is testable byte for byte. lettre picks
  random boundaries by default; set the boundary yourself.
- **HTML escaping.** Titles and bodies come from users; the HTML template
  must auto-escape. askama escapes `.html` templates by default; minijinja
  needs auto-escaping turned on for them.
- **SMTP classification.** 4xx replies are transient, 5xx permanent;
  connection failures and timeouts are transient. lettre exposes the reply
  code; map it with the same table as `smtp.go`.
- **Redis pub/sub needs its own connection** and is at most once; the hub
  must tolerate a missed message (clients reconcile with `ListInbox`).

## Done when

- every Go test above passes in Rust, the randomised email outbox test
  included;
- both conformance fixtures pass;
- the seed replay reproduces the expected inbox;
- `make e2e` passes with Rust notifications, notably
  `TestAMentionNotifiesLiveInTheInboxAndByEmail`.
