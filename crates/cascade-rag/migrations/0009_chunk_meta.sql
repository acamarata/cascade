-- Migration 0009: rag_chunk_meta — curated document-level metadata
--
-- Stores the hand-written frontmatter fields (description, title, tags) that
-- serve as a privileged retrieval surface in the curated-description RRF channel.
-- One row per source document (not per chunk); joined to rag_chunks via source_id.
--
-- The associated FTS5 virtual table (rag_chunk_meta_fts5) enables fast substring
-- and BM25 matching over these curated fields independently of the body FTS index.

CREATE TABLE IF NOT EXISTS rag_chunk_meta (
    id          INTEGER PRIMARY KEY,
    source_id   INTEGER NOT NULL UNIQUE REFERENCES rag_sources (id) ON DELETE CASCADE,
    description TEXT,
    title       TEXT,
    tags        TEXT,    -- space-separated tag tokens for BM25 weight amplification
    schema_version INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_chunk_meta_source_id ON rag_chunk_meta (source_id);

-- FTS5 virtual table for curated-description search.
-- Matches across description + title + tags simultaneously.
CREATE VIRTUAL TABLE IF NOT EXISTS rag_chunk_meta_fts5 USING fts5 (
    description,
    title,
    tags,
    content     = "rag_chunk_meta",
    content_rowid = "id",
    tokenize    = "porter ascii"
);

-- Sync triggers for rag_chunk_meta_fts5.
CREATE TRIGGER IF NOT EXISTS rag_chunk_meta_fts5_ai
    AFTER INSERT ON rag_chunk_meta
BEGIN
    INSERT INTO rag_chunk_meta_fts5 (rowid, description, title, tags)
        VALUES (new.id, new.description, new.title, new.tags);
END;

CREATE TRIGGER IF NOT EXISTS rag_chunk_meta_fts5_au
    AFTER UPDATE ON rag_chunk_meta
BEGIN
    INSERT INTO rag_chunk_meta_fts5 (rag_chunk_meta_fts5, rowid, description, title, tags)
        VALUES ('delete', old.id, old.description, old.title, old.tags);
    INSERT INTO rag_chunk_meta_fts5 (rowid, description, title, tags)
        VALUES (new.id, new.description, new.title, new.tags);
END;

CREATE TRIGGER IF NOT EXISTS rag_chunk_meta_fts5_bd
    BEFORE DELETE ON rag_chunk_meta
BEGIN
    INSERT INTO rag_chunk_meta_fts5 (rag_chunk_meta_fts5, rowid, description, title, tags)
        VALUES ('delete', old.id, old.description, old.title, old.tags);
END;
