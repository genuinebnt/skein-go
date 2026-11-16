-- Transactional outbox and idempotent-consumer tables (ADR-0006).
-- Copy into a service migration; see the outbox package documentation.

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

-- The claim scans unpublished rows in id order and looks up earlier
-- unpublished rows of the same key; both stay small because published rows
-- leave these partial indexes.
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox (id) WHERE published_at IS NULL;
CREATE INDEX IF NOT EXISTS outbox_pending_by_key ON outbox (ordering_key, id) WHERE published_at IS NULL;
CREATE INDEX IF NOT EXISTS outbox_published_at ON outbox (published_at) WHERE published_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS processed_events (
  consumer     text        NOT NULL,
  event_id     uuid        NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer, event_id)
);
