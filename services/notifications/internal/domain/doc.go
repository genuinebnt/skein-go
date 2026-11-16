// Package domain holds the notification service's entities and rules, with
// no I/O: notifications and their kinds, per-kind preferences, the @mention
// parser, the producer rules that turn facts (a comment, an access change,
// a finished agent run, a mention typed into a block) into candidate
// notifications, the email digest planner, retry backoff, and inbox page
// tokens.
//
// # Producer rules
//
// The rules follow docs/seed-data.md ("Notification rules"), which the seed's
// expected inbox (testdata/seed/*/notifications.jsonl) obeys:
//
//	fact                         recipients                              kind
//	comment                      every @mentioned user except the author MENTION
//	comment                      the note's creator; for a reply, also   COMMENT
//	                             the parent comment's author; except the
//	                             author and anyone already notified for
//	                             this comment
//	note role set for a guest    the guest                               SHARE
//	note role set for a member   the member                              ACCESS_CHANGED
//	note access revoked          the user                                ACCESS_CHANGED
//	member removed               the removed member                      ACCESS_CHANGED
//	agent run finished           the user the run acted for              AGENT_RUN_FINISHED
//	agent asks for confirmation  the user the run acted for              AGENT_RUN_FINISHED
//	@mention typed into a block  each newly mentioned user but the actor MENTION
//	NotificationRequested        its recipient                           its kind
//
// Rules return Candidates. A candidate that refers to a note carries
// NeedsRead, and the application layer drops it unless the recipient can
// read the note at the time of the fact: "a mention of someone who cannot
// read the note sends nothing". The comment rule deduplicates recipients
// before that check, first match wins (mentions before comments). That is
// the same as the seed's "unless already notified" because both kinds need
// the same permission on the same note at the same instant: a mentioned
// user who fails the check would fail it for COMMENT too.
//
// # Identity of a notification
//
// Source says what caused a notification (a comment ID, a note and user for
// a role change, an agent run). The store keeps at most one notification
// per (recipient, source), which makes producers idempotent beyond the
// event-ID deduplication of the consumers, and is what the seed compares
// (set equality on user, kind and source).
//
// CoalesceKey says what a notification is *about*, for deduplicating bursts:
// ten edits that each mention Ada in the same note within the window become
// one unread notification whose Occurrences counts them. Facts with a unique
// source of their own (comments, role changes, agent runs) are never
// coalesced, because each one is a distinct thing the user should see.
//
// # Invariants
//
// Notification.Validate checks one notification; Preferences and the email
// plan have CheckInvariants. Times are truncated to microseconds when a
// notification is built, because Postgres stores microseconds and page
// tokens must round-trip exactly.
//
// # Porting notes
//
// Kind is a fixed-width enum with the proto's numbers. Candidate, Source and
// Location are plain structs (Rust structs with Option for the empty
// strings that mean "none"). ReadAt's zero value means unread
// (Option<DateTime<Utc>>). The mention parser works on runes and the
// excerpt counts code points (ADR-0003). Nothing here iterates a map to
// produce output; maps are only used for membership.
package domain
