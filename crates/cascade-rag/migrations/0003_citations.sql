-- Migration 0003: rag_citations
-- Audit log of retrieval results per query, storing per-pipeline scores for
-- offline evaluation and RRF weight tuning.
CREATE TABLE IF NOT EXISTS rag_citations (
    id           INTEGER PRIMARY KEY,
    query_hash   TEXT    NOT NULL,
    chunk_id     INTEGER NOT NULL REFERENCES rag_chunks (id) ON DELETE CASCADE,
    rrf_score    REAL,
    dense_score  REAL,
    sparse_score REAL,
    fts5_score   REAL,
    retrieved_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_citations_query_hash ON rag_citations (query_hash);
