-- Migration 0004: rag_embeddings + rag_sparse_embeddings
-- Requires: sqlite-vec extension loaded at runtime (feature = "vec").
-- rag_embeddings uses vec0 virtual table with float[1024] matching BGE-M3
-- dense output dimension. The vec0 rowid maps 1-to-1 to rag_chunks.id.
CREATE VIRTUAL TABLE IF NOT EXISTS rag_embeddings USING vec0 (
    rowid      INTEGER PRIMARY KEY,
    embedding  float[1024]
);

-- rag_sparse_embeddings stores SPLADE-style sparse token weights for
-- BM25 approximation and future hybrid scoring extensions.
CREATE TABLE IF NOT EXISTS rag_sparse_embeddings (
    chunk_id  INTEGER NOT NULL,
    token_id  INTEGER NOT NULL,
    weight    REAL    NOT NULL,
    PRIMARY KEY (chunk_id, token_id)
);
