// Package app holds the notification service's use cases and the ports
// they need: the inbox (ListInbox, MarkRead, preferences), the producer
// that turns events into notifications, the live hub behind Subscribe, and
// the email sender that drains the email outbox.
//
// # Producing a notification
//
// Every event goes through Producer.apply in two phases:
//
//  1. Outside any transaction, look up what the rules need from other
//     services: display names and handles (Directory) and whether each
//     recipient can read the note (Authorizer). Network calls never run
//     while a database transaction is open.
//
//  2. In one transaction: record the event as processed for this consumer
//     (a redelivery stops here, with outcome "duplicate"), update the
//     projections, and place each candidate the domain rules returned:
//
//     - drop it if the recipient cannot read its note, or turned both
//     channels off for its kind;
//     - if the recipient already has a notification from the same source
//     (the same comment, the same note role), refresh that one when the
//     new fact is newer: same ID, new text, unread again, created at the
//     fact's time, one more occurrence (a second role change on one note
//     is news; a redelivered one is not);
//     - else, if it has a coalescing key and the recipient has an unread
//     notification with that key from within the window, absorb it there
//     (one more occurrence, latest body, no push and no second email);
//     - else insert it, and when the recipient wants email for the kind,
//     enqueue an email job in the same transaction.
//
// After the commit the producer invalidates the cached unread counts of
// the recipients whose inbox changed, publishes new inbox entries to the
// live fan-out, and wakes the email sender. These are best effort: a
// failure is logged, not retried, because the commit already happened
// (the cache has a short TTL, and a client that missed a push reconciles
// with ListInbox).
//
// # Email outbox and its guarantee
//
// Email jobs are written in the transaction that creates their
// notification, so an email is never lost: if the notification exists,
// its job does. EmailSender then works in three short transactions:
//
//  1. plan: group due jobs into sends (one email, or a digest when several
//     are pending for one recipient; see domain.PlanEmails), skipping jobs
//     whose notification was read in the meantime;
//  2. claim: lease due sends (state sending, one more attempt);
//  3. send through the Mailer, then record sent, a retry with exponential
//     backoff and jitter, or dead (after MaxAttempts, or at once for a
//     permanent failure such as an SMTP 5xx). Dead sends stay in the table
//     with their last error: that is the dead-letter path, counted in
//     skein.notifications.emails{outcome="dead_lettered"}.
//
// SMTP has no idempotent submission, so exactly-once is not achievable.
// What this achieves is at-least-once with a single, narrow duplicate
// window: an email is sent twice only if the process dies (or loses the
// database) after the SMTP server accepted the message and before the
// "sent" mark commits. Then the lease expires and the send is retried with
// the same Message-ID, which clients and Gmail use to fold duplicates. A
// crash anywhere else causes no duplicate: before the SMTP DATA reply
// nothing was accepted, and after the mark nothing is retried. A sender
// that hangs past its lease is the same window seen from outside: another
// sender may take the send over and send it again. A late failure cannot
// overwrite the outcome of the sender that took over (FinishEmailSend
// checks the attempt number), but a late success is always recorded, so
// it stops further retries. The randomised test
// in emailsender_test.go injects crashes at every step and checks exactly
// this: no job lost, duplicates only after a crash in that window, each
// duplicate with the same Message-ID.
//
// # Porting notes
//
// Use cases take ports as interfaces (Rust traits behind Arc<dyn _>). The
// transaction callback becomes an async closure taking &mut dyn Tx. The
// hub's goroutine-free design (channels per subscriber, a mutex for the
// registry) maps to Tokio mpsc senders in a Mutex<HashMap>.
package app
