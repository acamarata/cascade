-- Migration 0002: rag_chunks
-- One row per chunk produced by a Chunker. parent_chunk_id is nullable to
-- support both flat and hierarchical chunking strategies.
CREATE TABLE IF NOT EXISTS rag_chunks (
    id             INTEGER PRIMARY KEY,
    source_id      INTEGER NOT NULL REFERENCES rag_sources (id) ON DELETE CASCADE,
    chunk_index    INTEGER NOT NULL,
    chunk_text     TEXT    NOT NULL,
    char_start     INTEGER,
    line_start     INTEGER,
    line_end       INTEGER,
    parent_chunk_id INTEGER REFERENCES rag_chunks (id) ON DELETE SET NULL,
    heading_path   TEXT,
    schema_version INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_chunks_source_id      ON rag_chunks (source_id);
CREATE INDEX IF NOT EXISTS idx_chunks_parent_chunk_id ON rag_chunks (parent_chunk_id);
