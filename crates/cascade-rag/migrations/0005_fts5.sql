-- Migration 0005: rag_fts5 full-text search virtual table
-- External content table backed by rag_chunks (content_rowid = id).
-- Porter stemming over ASCII enables prefix and stem matching.
CREATE VIRTUAL TABLE IF NOT EXISTS rag_fts5 USING fts5 (
    chunk_text,
    content     = "rag_chunks",
    content_rowid = "id",
    tokenize    = "porter ascii"
);

-- Sync triggers: keep rag_fts5 consistent with rag_chunks writes.
-- ai = after insert, au = after update, bd = before delete.
CREATE TRIGGER IF NOT EXISTS rag_fts5_ai
    AFTER INSERT ON rag_chunks
BEGIN
    INSERT INTO rag_fts5 (rowid, chunk_text) VALUES (new.id, new.chunk_text);
END;

CREATE TRIGGER IF NOT EXISTS rag_fts5_au
    AFTER UPDATE ON rag_chunks
BEGIN
    INSERT INTO rag_fts5 (rag_fts5, rowid, chunk_text) VALUES ('delete', old.id, old.chunk_text);
    INSERT INTO rag_fts5 (rowid, chunk_text) VALUES (new.id, new.chunk_text);
END;

CREATE TRIGGER IF NOT EXISTS rag_fts5_bd
    BEFORE DELETE ON rag_chunks
BEGIN
    INSERT INTO rag_fts5 (rag_fts5, rowid, chunk_text) VALUES ('delete', old.id, old.chunk_text);
END;
