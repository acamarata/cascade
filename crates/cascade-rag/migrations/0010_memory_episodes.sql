-- Migration 0010: memory_episodes — episodic memory store for the RAG-08 memory module
--
-- Each row is a single memory episode scoped to a namespace (personal, dev-<slug>, meta).
-- Hard isolation: queries MUST filter by namespace (SQL WHERE namespace = ?).
-- Cross-namespace leakage is forbidden by application logic in memory/recall.rs.
--
-- Embedding is stored as a BLOB (optional; NULL when not yet embedded).
-- The embedding dimension matches Multilingual E5 Large output
-- (1024 floats × 4 bytes = 4096 B).
--
-- SPORT: MASTER-TABLES.md → memory_episodes (RAG-08)

CREATE TABLE IF NOT EXISTS memory_episodes (
    id           TEXT NOT NULL PRIMARY KEY,        -- UUID v4
    namespace    TEXT NOT NULL,                    -- 'personal' | 'dev-<slug>' | 'meta'
    content      TEXT NOT NULL,                    -- raw episode text
    embedding    BLOB,                             -- E5 Large dense float[1024], may be NULL
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_mem_eps_namespace ON memory_episodes (namespace);
CREATE INDEX IF NOT EXISTS idx_mem_eps_created   ON memory_episodes (namespace, created_at DESC);
