-- Migration 0004 (no-vec fallback): rag_embeddings as plain BLOB table + rag_sparse_embeddings
-- Used when the `vec` feature is disabled. Stores dense vectors as raw
-- little-endian IEEE 754 BLOB; KNN is performed in Rust via full table scan.
CREATE TABLE IF NOT EXISTS rag_embeddings (
    rowid      INTEGER PRIMARY KEY,
    embedding  BLOB NOT NULL
);

-- rag_sparse_embeddings stores SPLADE-style sparse token weights for
-- BM25 approximation and future hybrid scoring extensions.
CREATE TABLE IF NOT EXISTS rag_sparse_embeddings (
    chunk_id  INTEGER NOT NULL,
    token_id  INTEGER NOT NULL,
    weight    REAL    NOT NULL,
    PRIMARY KEY (chunk_id, token_id)
);
