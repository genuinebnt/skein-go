-- ml service schema: the note projection, block embeddings and processed
-- event ids. Rebuildable from documents.events; nothing here is a source of
-- truth for note content.
CREATE SCHEMA IF NOT EXISTS ml;

CREATE TABLE IF NOT EXISTS ml.notes (
    note_id      text PRIMARY KEY,
    workspace_id text NOT NULL DEFAULT '',
    title        text NOT NULL DEFAULT '',
    trashed      boolean NOT NULL DEFAULT false,
    links        text[] NOT NULL DEFAULT '{}',
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS notes_by_workspace ON ml.notes (workspace_id, note_id);

CREATE TABLE IF NOT EXISTS ml.blocks (
    note_id  text NOT NULL REFERENCES ml.notes (note_id) ON DELETE CASCADE,
    block_id text NOT NULL,
    body     text NOT NULL,
    PRIMARY KEY (note_id, block_id)
);

-- Vectors are little-endian float32 (numpy tobytes), dimension recorded so a
-- reader can validate without knowing the model.
CREATE TABLE IF NOT EXISTS ml.block_embeddings (
    note_id   text NOT NULL,
    model_id  text NOT NULL,
    block_id  text NOT NULL,
    dimension integer NOT NULL CHECK (dimension > 0),
    vector    bytea NOT NULL CHECK (length(vector) = 4 * dimension),
    PRIMARY KEY (note_id, model_id, block_id)
);

CREATE TABLE IF NOT EXISTS ml.processed_events (
    event_id     text PRIMARY KEY,
    processed_at timestamptz NOT NULL DEFAULT now()
);
