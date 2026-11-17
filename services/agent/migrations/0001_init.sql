-- agent service schema: chats and their transcripts, runs with their status,
-- paused state and usage, and run traces. LangGraph's checkpoint tables
-- live in this schema too, created by its own migrations (AsyncPostgresSaver
-- .setup()) because the service connects with search_path=agent.
-- The agent schema itself is created by migrate() when missing: CREATE SCHEMA
-- IF NOT EXISTS would need the database CREATE privilege, which the
-- service role does not have (deploy/compose/postgres/init.sql).

CREATE TABLE IF NOT EXISTS agent.chats (
    chat_id       text PRIMARY KEY,
    user_id       text NOT NULL,
    workspace_id  text NOT NULL,
    note_id       text NOT NULL DEFAULT '',
    title         text NOT NULL DEFAULT '',
    updated_at_ms bigint NOT NULL
);
-- ListChats: a user's chats in a workspace, most recently updated first.
CREATE INDEX IF NOT EXISTS chats_by_user
    ON agent.chats (user_id, workspace_id, updated_at_ms DESC, chat_id);

CREATE TABLE IF NOT EXISTS agent.chat_turns (
    chat_id text NOT NULL REFERENCES agent.chats (chat_id) ON DELETE CASCADE,
    seq     integer NOT NULL CHECK (seq > 0),
    role    text NOT NULL CHECK (role IN ('user', 'assistant')),
    text    text NOT NULL,
    PRIMARY KEY (chat_id, seq)
);

-- status moves running -> paused -> running ... -> finished. Every move out
-- of paused is an UPDATE ... WHERE status = 'paused', so exactly one process
-- wins it (app/ports.py RunStore).
CREATE TABLE IF NOT EXISTS agent.runs (
    run_id           text PRIMARY KEY,
    chat_id          text NOT NULL,
    user_id          text NOT NULL,
    workspace_id     text NOT NULL,
    status           text NOT NULL CHECK (status IN ('running', 'paused', 'finished')),
    created_at_ms    bigint NOT NULL,
    -- When the current running stretch began; a run running far past its
    -- wall-time budget belongs to a stopped process.
    running_since_ms bigint NOT NULL,
    paused_at_ms     bigint,
    confirmation_id  text,
    -- PausedRun as JSON (adapters/codec.py); never holds a token.
    paused_state     jsonb,
    finished_at_ms   bigint,
    steps            integer,
    input_tokens     bigint,
    output_tokens    bigint,
    cost_usd         double precision,
    CHECK ((status = 'paused') = (paused_state IS NOT NULL)),
    CHECK ((status = 'finished') = (finished_at_ms IS NOT NULL))
);
-- One unfinished run per chat, across every replica.
CREATE UNIQUE INDEX IF NOT EXISTS runs_one_unfinished_per_chat
    ON agent.runs (chat_id) WHERE status <> 'finished';
CREATE INDEX IF NOT EXISTS runs_paused ON agent.runs (paused_at_ms) WHERE status = 'paused';
CREATE INDEX IF NOT EXISTS runs_running ON agent.runs (running_since_ms) WHERE status = 'running';

CREATE TABLE IF NOT EXISTS agent.run_entries (
    run_id text NOT NULL REFERENCES agent.runs (run_id) ON DELETE CASCADE,
    seq    integer NOT NULL CHECK (seq > 0),
    at_ms  double precision NOT NULL,
    -- One trace entry as JSON with its kind tag (adapters/codec.py).
    entry  jsonb NOT NULL,
    PRIMARY KEY (run_id, seq)
);
