-- Identity's schema (ADR-0008). Tables are unqualified: the identity role's
-- search_path is the identity schema.

CREATE TABLE users (
  id                uuid        PRIMARY KEY,
  -- Normalised (trimmed, lowercase) by the app; the check keeps it so.
  email             text        NOT NULL UNIQUE CHECK (email = lower(email) AND email = btrim(email)),
  display_name      text        NOT NULL,
  -- PHC Argon2id string, or '' for accounts without a password.
  password_hash     text        NOT NULL DEFAULT '',
  email_verified_at timestamptz,
  created_at        timestamptz NOT NULL
);

CREATE TABLE oauth_identities (
  provider   text        NOT NULL CHECK (provider IN ('google', 'github')),
  -- The provider's stable account ID, never the email.
  subject    text        NOT NULL,
  user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (provider, subject)
);
CREATE INDEX oauth_identities_by_user ON oauth_identities (user_id);

-- A session is one sign-in and the chain of refresh tokens rotated from it.
CREATE TABLE sessions (
  id            uuid        PRIMARY KEY,
  user_id       uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  created_at    timestamptz NOT NULL,
  revoked_at    timestamptz,
  revoke_reason text        NOT NULL DEFAULT ''
);
CREATE INDEX sessions_live_by_user ON sessions (user_id) WHERE revoked_at IS NULL;

CREATE TABLE refresh_tokens (
  -- Hex SHA-256 of the opaque token; the token itself is never stored.
  hash       text        PRIMARY KEY CHECK (length(hash) = 64),
  session_id uuid        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
  issued_at  timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  rotated_at timestamptz
);
CREATE INDEX refresh_tokens_by_session ON refresh_tokens (session_id);
-- Lets a periodic job delete long-expired rows without a full scan.
CREATE INDEX refresh_tokens_by_expiry ON refresh_tokens (expires_at);

CREATE TABLE workspaces (
  id         uuid        PRIMARY KEY,
  name       text        NOT NULL,
  owner_id   uuid        NOT NULL REFERENCES users (id),
  created_at timestamptz NOT NULL
);

-- One row per (workspace, user). Removal sets removed_at; adding the user
-- back starts a new membership in the same row.
CREATE TABLE memberships (
  workspace_id uuid        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
  user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  -- skein.v1.WorkspaceRole: 1 guest, 2 member, 3 admin, 4 owner.
  role         smallint    NOT NULL CHECK (role BETWEEN 1 AND 4),
  joined_at    timestamptz NOT NULL,
  removed_at   timestamptz,
  invited_by   uuid,
  PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX memberships_active_by_user ON memberships (user_id) WHERE removed_at IS NULL;
CREATE INDEX memberships_active_owners ON memberships (workspace_id) WHERE removed_at IS NULL AND role = 4;

-- Identity's projection of documents' notes: only what the policy needs.
-- No foreign key to workspaces: it is fed by events from another service,
-- and a projection must accept what its source says.
CREATE TABLE notes (
  id           uuid        PRIMARY KEY,
  workspace_id uuid        NOT NULL,
  created_by   uuid        NOT NULL,
  created_at   timestamptz NOT NULL,
  trashed_at   timestamptz
);
CREATE INDEX notes_by_workspace ON notes (workspace_id, id);

CREATE TABLE note_role_overrides (
  note_id      uuid        NOT NULL,
  user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  -- The note's workspace, so removing a member drops their overrides in one
  -- statement.
  workspace_id uuid        NOT NULL,
  -- skein.v1.NoteRole: 1 viewer, 2 commenter, 3 editor.
  role         smallint    NOT NULL CHECK (role BETWEEN 1 AND 3),
  granted_by   uuid,
  created_at   timestamptz NOT NULL,
  PRIMARY KEY (note_id, user_id)
);
CREATE INDEX note_role_overrides_by_workspace_user ON note_role_overrides (workspace_id, user_id);

CREATE TABLE share_links (
  id           uuid        PRIMARY KEY,
  note_id      uuid        NOT NULL,
  workspace_id uuid        NOT NULL,
  -- New links are viewer or commenter; 3 (editor) exists only in loaded
  -- legacy data, and the policy clamps it to commenter.
  role         smallint    NOT NULL CHECK (role BETWEEN 1 AND 3),
  token_hash   text        NOT NULL UNIQUE CHECK (length(token_hash) = 64),
  created_by   uuid,
  created_at   timestamptz NOT NULL,
  expires_at   timestamptz,
  revoked_at   timestamptz
);
CREATE INDEX share_links_by_note ON share_links (note_id);

CREATE TABLE replicas (
  -- The replica's unsigned 64 bits stored as a signed bigint (two's
  -- complement), so IDs at or above 2^63 read as negative in SQL. 0 is
  -- reserved by the CRDT spec.
  id            bigint      PRIMARY KEY CHECK (id <> 0),
  user_id       uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  agent_run_id  uuid,
  registered_at timestamptz NOT NULL
);
CREATE INDEX replicas_by_user ON replicas (user_id);

CREATE TABLE one_time_tokens (
  hash       text        PRIMARY KEY CHECK (length(hash) = 64),
  user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  purpose    text        NOT NULL CHECK (purpose IN ('email_verification', 'password_reset')),
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  used_at    timestamptz
);

-- Transactional outbox and idempotent-consumer tables, copied from
-- platform/outbox/schema.sql (ADR-0006).
CREATE TABLE outbox (
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
CREATE INDEX outbox_pending ON outbox (id) WHERE published_at IS NULL;
CREATE INDEX outbox_pending_by_key ON outbox (ordering_key, id) WHERE published_at IS NULL;
CREATE INDEX outbox_published_at ON outbox (published_at) WHERE published_at IS NOT NULL;

CREATE TABLE processed_events (
  consumer     text        NOT NULL,
  event_id     uuid        NOT NULL,
  processed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (consumer, event_id)
);
