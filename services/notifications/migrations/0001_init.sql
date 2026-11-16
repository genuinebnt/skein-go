-- The notifications service's schema (ADR-0001: only this service reads or
-- writes it). Tables are created in the role's own schema through its
-- search_path (deploy/compose/postgres/init.sql).

-- The inbox. One row per notification; email-only notifications have
-- in_inbox = false and never show in ListInbox, the unread count or push.
CREATE TABLE notifications (
  id            uuid        PRIMARY KEY,
  recipient_id  uuid        NOT NULL,
  kind          smallint    NOT NULL CHECK (kind BETWEEN 1 AND 5),
  title         text        NOT NULL CHECK (title <> ''),
  body          text        NOT NULL DEFAULT '',
  note_id       uuid,
  block_id      text        NOT NULL DEFAULT '',
  range_start   integer     NOT NULL DEFAULT 0 CHECK (range_start >= 0),
  range_end     integer     NOT NULL DEFAULT 0 CHECK (range_end >= range_start),
  actor_id      uuid,
  source_kind   text        NOT NULL,
  source_id     text        NOT NULL,
  coalesce_key  text        NOT NULL DEFAULT '',
  occurrences   integer     NOT NULL DEFAULT 1 CHECK (occurrences >= 1),
  in_inbox      boolean     NOT NULL,
  created_at    timestamptz NOT NULL,
  updated_at    timestamptz NOT NULL CHECK (updated_at >= created_at),
  read_at       timestamptz,
  -- A recipient has at most one notification per cause (domain.Source).
  UNIQUE (recipient_id, source_kind, source_id)
);

-- ListInbox: newest first by (created_at, id), optionally unread only.
CREATE INDEX notifications_inbox ON notifications (recipient_id, created_at DESC, id DESC) WHERE in_inbox;
CREATE INDEX notifications_unread ON notifications (recipient_id, created_at DESC, id DESC) WHERE in_inbox AND read_at IS NULL;
-- Coalescing looks for the newest unread row with a key.
CREATE INDEX notifications_coalesce ON notifications (recipient_id, coalesce_key, created_at DESC)
  WHERE coalesce_key <> '' AND read_at IS NULL;
-- NotePurged deletes by note.
CREATE INDEX notifications_note ON notifications (note_id) WHERE note_id IS NOT NULL;

-- Per-kind overrides of the defaults in domain.DefaultPreference. A user
-- with no rows has the defaults.
CREATE TABLE preferences (
  user_id     uuid        NOT NULL,
  kind        smallint    NOT NULL,
  in_app      boolean     NOT NULL,
  email       boolean     NOT NULL,
  updated_at  timestamptz NOT NULL,
  PRIMARY KEY (user_id, kind)
);

-- Projection of documents.events: what notifications need about a note.
CREATE TABLE notes (
  id            uuid    PRIMARY KEY,
  workspace_id  uuid,
  title         text    NOT NULL DEFAULT '',
  created_by    uuid,
  trashed       boolean NOT NULL DEFAULT false
);

-- Projection of BlocksChanged: who each block's text mentions, so an edit
-- notifies only users it newly mentions.
CREATE TABLE block_mentions (
  note_id   uuid NOT NULL,
  block_id  text NOT NULL,
  user_id   uuid NOT NULL,
  PRIMARY KEY (note_id, block_id, user_id)
);

-- The email outbox (app package documentation). A send is one email: a
-- single notification or a digest.
CREATE TABLE email_sends (
  id               uuid        PRIMARY KEY,
  recipient_id     uuid        NOT NULL,
  state            text        NOT NULL CHECK (state IN ('pending', 'sending', 'sent', 'dead')),
  attempts         integer     NOT NULL DEFAULT 0,
  next_attempt_at  timestamptz NOT NULL,
  lease_until      timestamptz,
  last_error       text        NOT NULL DEFAULT '',
  created_at       timestamptz NOT NULL,
  sent_at          timestamptz,
  CHECK (state <> 'sending' OR lease_until IS NOT NULL),
  CHECK (state <> 'sent' OR sent_at IS NOT NULL)
);
CREATE INDEX email_sends_due ON email_sends (next_attempt_at, id) WHERE state = 'pending';
CREATE INDEX email_sends_leased ON email_sends (lease_until, id) WHERE state = 'sending';

-- One job per notification to email, written in the notification's
-- transaction. Deleting a notification (NotePurged) deletes its jobs.
CREATE TABLE email_jobs (
  id               uuid        PRIMARY KEY,
  recipient_id     uuid        NOT NULL,
  notification_id  uuid        NOT NULL REFERENCES notifications (id) ON DELETE CASCADE,
  state            text        NOT NULL CHECK (state IN ('pending', 'assigned', 'skipped')),
  send_id          uuid        REFERENCES email_sends (id),
  created_at       timestamptz NOT NULL,
  due_at           timestamptz NOT NULL,
  CHECK ((state = 'assigned') = (send_id IS NOT NULL))
);
-- At most one unassigned job per notification (EnqueueEmail relies on it).
CREATE UNIQUE INDEX email_jobs_one_pending ON email_jobs (notification_id) WHERE state = 'pending';
CREATE INDEX email_jobs_due ON email_jobs (recipient_id, due_at) WHERE state = 'pending';
CREATE INDEX email_jobs_send ON email_jobs (send_id) WHERE send_id IS NOT NULL;

-- Idempotent consumers: copied from platform/outbox/schema.sql (a copy,
-- because an applied migration must never change). The outbox table itself
-- is not needed: this service publishes no events.
CREATE TABLE processed_events (
  consumer     text        NOT NULL,
  event_id     uuid        NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer, event_id)
);
