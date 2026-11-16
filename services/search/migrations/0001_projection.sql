-- The search service's durable projection (ADR-0006, ADR-0010). Everything
-- here is a read model of documents and identity events: it can be rebuilt
-- by replaying them (services/search/README.md, "Rebuilding").

-- Idempotent consumers: one row per processed event and consumer, written
-- in the transaction that applies the event (copied from
-- platform/outbox/schema.sql; search publishes nothing, so no outbox).
CREATE TABLE processed_events (
  consumer     text        NOT NULL,
  event_id     uuid        NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer, event_id)
);

-- Each consumer's last applied event, for operators and the rebuild path.
CREATE TABLE projection_positions (
  consumer       text        PRIMARY KEY,
  last_event_id  text        NOT NULL,
  events_applied bigint      NOT NULL DEFAULT 0,
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- Note states. A purged note keeps a tombstone row (purged, nothing else)
-- so late or replayed events for it are ignored.
CREATE TABLE notes (
  note_id      text    PRIMARY KEY,
  workspace_id text    NOT NULL DEFAULT '',
  title        text    NOT NULL DEFAULT '',
  trashed      boolean NOT NULL DEFAULT false,
  is_root      boolean NOT NULL DEFAULT false,
  purged       boolean NOT NULL DEFAULT false
);

CREATE TABLE blocks (
  note_id  text    NOT NULL REFERENCES notes (note_id) ON DELETE CASCADE,
  block_id text    NOT NULL,
  ordinal  integer NOT NULL,
  kind     text    NOT NULL DEFAULT '',
  language text    NOT NULL DEFAULT '',
  body     text    NOT NULL DEFAULT '',
  PRIMARY KEY (note_id, block_id)
);

-- Outbound links, already without self-links and repeats.
CREATE TABLE links (
  note_id   text NOT NULL REFERENCES notes (note_id) ON DELETE CASCADE,
  target_id text NOT NULL,
  PRIMARY KEY (note_id, target_id)
);

-- The change feed every replica follows. Positions come from the single
-- feed_state row: incrementing it takes a row lock held to commit, so
-- writers are serialized and positions are gapless and in commit order. A
-- follower that has read up to position P can never miss a change that
-- commits later with a smaller position.
CREATE TABLE feed_state (
  id             integer PRIMARY KEY CHECK (id = 1),
  last_seq       bigint  NOT NULL DEFAULT 0,
  -- Changes up to here were pruned; a follower behind it must reload.
  pruned_through bigint  NOT NULL DEFAULT 0
);
INSERT INTO feed_state (id) VALUES (1);

CREATE TABLE changes (
  seq        bigint      PRIMARY KEY,
  note_id    text        NOT NULL,
  changed_at timestamptz NOT NULL DEFAULT now()
);

-- Encoded projections (services/search/internal/domain/snapshot.go) and the
-- feed position each reflects. They hold note text, so a purge deletes them.
CREATE TABLE snapshots (
  position   bigint      PRIMARY KEY,
  format     integer     NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  data       bytea       NOT NULL
);
