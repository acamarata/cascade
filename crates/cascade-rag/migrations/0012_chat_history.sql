-- Migration 0012: chat_history — per-scope, per-namespace chat message log
--
-- Stores chat turns for the cascade memory system. Scope identifies the product
-- surface (e.g. "cascade-app", "mcp-session") and namespace provides memory isolation.
-- Role must be "user" or "assistant" (enforced by application layer).
--
-- The composite index (scope, namespace, created_at DESC) supports the canonical
-- list_chat(scope, namespace, limit) query efficiently.
--
-- SPORT: MASTER-TABLES.md → chat_history (RAG-08)

CREATE TABLE IF NOT EXISTS chat_history (
    id           TEXT NOT NULL PRIMARY KEY,        -- UUID v4
    scope        TEXT NOT NULL,                    -- e.g. "cascade-app", "mcp-session"
    namespace    TEXT NOT NULL,                    -- 'personal' | 'dev-<slug>' | 'meta'
    role         TEXT NOT NULL,                    -- 'user' | 'assistant'
    content      TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS idx_chat_hist_ns ON chat_history (scope, namespace, created_at DESC);
