-- Migration 0007: index_state
-- Tracks per-file delta metadata in a separate cascade_state.db so the main
-- RAG database is not burdened with filesystem-state writes on every pass.
-- Columns:
--   doc_id        — stable identifier: hex-encoded Blake3 of file_path string
--   file_path     — absolute or project-relative path
--   content_hash  — hex Blake3 of file bytes (canonical change signal)
--   mtime         — Unix seconds from file metadata (fast-skip pre-check)
--   byte_size     — file size in bytes (fast-skip pre-check alongside mtime)
--   indexed_at    — Unix seconds when this row was last written
CREATE TABLE IF NOT EXISTS index_state (
    doc_id        TEXT    PRIMARY KEY,
    file_path     TEXT    NOT NULL UNIQUE,
    content_hash  TEXT    NOT NULL,
    mtime         INTEGER,
    byte_size     INTEGER,
    indexed_at    INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_index_state_path ON index_state (file_path);
