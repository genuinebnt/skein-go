-- The documents schema (ADR-0006). The connection's search_path selects the
-- schema (deploy/compose/postgres/init.sql), so names are unqualified.
--
-- Unsigned 64-bit CRDT integers (replica IDs, counters) are stored in bigint
-- by bit pattern: Go converts with int64(u) and uint64(i). Postgres has no
-- unsigned type and numeric would be slower; nothing compares them in SQL
-- except for equality.

CREATE TABLE notes (
  id           uuid        PRIMARY KEY,
  workspace_id uuid        NOT NULL,
  title        text        NOT NULL,
  created_by   text        NOT NULL,
  created_at   timestamptz NOT NULL,
  updated_at   timestamptz NOT NULL,
  trashed_at   timestamptz,
  -- A purged note keeps this row, emptied, so its ID stays NOT_FOUND and
  -- cannot be reused.
  purged_at    timestamptz,
  -- The op log's last batch, so stale projections are found without a scan.
  last_seq     bigint      NOT NULL DEFAULT 0
);
CREATE INDEX notes_by_workspace ON notes (workspace_id, id) WHERE purged_at IS NULL;
CREATE INDEX notes_in_trash ON notes (trashed_at) WHERE trashed_at IS NOT NULL AND purged_at IS NULL;

-- The op log: append-only, one row per batch. ops is a batch in the CRDT
-- wire format v1 (docs/spec/crdt-sequence.md), attrs in the attribute
-- format (docs/spec/blocks.md). Rows are only ever deleted by a purge.
CREATE TABLE op_batches (
  note_id       uuid        NOT NULL REFERENCES notes (id),
  seq           bigint      NOT NULL CHECK (seq > 0),
  replica_id    bigint      NOT NULL,
  first_counter bigint      NOT NULL,
  last_counter  bigint      NOT NULL,
  for_replica   bigint      NOT NULL,
  group_seq     bigint      NOT NULL,
  user_id       text        NOT NULL,
  agent_run_id  text        NOT NULL,
  kind          smallint    NOT NULL,
  ref_seq       bigint      NOT NULL,
  at            timestamptz NOT NULL,
  ops           bytea,
  attrs         bytea,
  -- The primary key is what makes a second writer for the same note fail
  -- instead of interleaving batches.
  PRIMARY KEY (note_id, seq)
);

-- Checkpoints: every op and attribute write through through_seq.
CREATE TABLE snapshots (
  note_id     uuid        NOT NULL REFERENCES notes (id),
  through_seq bigint      NOT NULL,
  ops         bytea       NOT NULL,
  attrs       bytea       NOT NULL,
  created_at  timestamptz NOT NULL,
  PRIMARY KEY (note_id, through_seq)
);

-- What the last BlocksChanged and LinksChanged told consumers.
CREATE TABLE projections (
  note_id     uuid   PRIMARY KEY REFERENCES notes (id),
  through_seq bigint NOT NULL,
  digests     jsonb  NOT NULL,
  links       text[] NOT NULL
);

-- Transactional outbox and idempotent-consumer tables, copied from
-- platform/outbox/schema.sql (a copy, because applied migrations never
-- change).
CREATE TABLE IF NOT EXISTS outbox (
  id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  topic           text        NOT NULL,
  event_id        uuid        NOT NULL UNIQUE,
  source          text        NOT NULL,
  ordering_key    text        NOT NULL DEFAULT '',
  traceparent     text        NOT NULL DEFAULT '',
  attributes      jsonb       NOT NULL DEFAULT '{}',
  payload         bytea       NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  attempts        integer     NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error      text,
  published_at    timestamptz
);
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox (id) WHERE published_at IS NULL;
CREATE INDEX IF NOT EXISTS outbox_pending_by_key ON outbox (ordering_key, id) WHERE published_at IS NULL;
CREATE INDEX IF NOT EXISTS outbox_published_at ON outbox (published_at) WHERE published_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS processed_events (
  consumer     text        NOT NULL,
  event_id     uuid        NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer, event_id)
);
