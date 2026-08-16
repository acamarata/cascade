-- Migration 0006: rag_token_embeddings
-- Adds per-word E5 proxy vector storage for MaxSim late interaction.
-- This is not native BGE-M3 ColBERT output.
-- Gated behind feature flag `rag-multivec` — this migration only runs when the feature
-- is enabled at runtime (see db/migrations.rs).
--
-- Disk usage: ~3-4x the dense embedding table. Warn users in cascade.toml docs.
-- Cascade on-delete ensures token vectors are cleaned up when their parent chunk is deleted.
--
-- SPORT: MASTER-TABLES.md → rag_token_embeddings

CREATE TABLE IF NOT EXISTS rag_token_embeddings (
    id        INTEGER PRIMARY KEY,
    chunk_id  INTEGER NOT NULL REFERENCES rag_chunks(id) ON DELETE CASCADE,
    token_idx INTEGER NOT NULL,
    vector    BLOB    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_rte_chunk ON rag_token_embeddings(chunk_id);
