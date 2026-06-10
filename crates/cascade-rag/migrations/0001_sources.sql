-- Migration 0001: rag_sources
-- Tracks every indexed file with its hash, mtime, parser used, and schema version.
CREATE TABLE IF NOT EXISTS rag_sources (
    id             INTEGER PRIMARY KEY,
    file_path      TEXT    NOT NULL UNIQUE,
    content_hash   TEXT,
    mtime          INTEGER,
    byte_size      INTEGER,
    parser         TEXT,
    indexed_at     INTEGER,
    schema_version INTEGER NOT NULL DEFAULT 1
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_sources_path ON rag_sources (file_path);
