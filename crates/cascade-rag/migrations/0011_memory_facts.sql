-- Migration 0011: memory_facts — consolidated factual memory for the RAG-08 memory module
--
-- Facts are distilled from episodes after N observations of the same content_hash
-- (consolidation). Each fact has a subject/predicate/value triple plus confidence,
-- a BLAKE3 content_hash for dedup, and a last_seen timestamp for decay.
--
-- Unique constraint on content_hash enforces at-most-one row per unique (namespace, fact).
-- content_hash is scoped by application logic to include the namespace prefix so that
-- two namespaces with identical factual text produce different hashes.
--
-- archived=TRUE means the fact is soft-deleted (decayed) but retained for audit.
-- Application queries MUST filter WHERE archived = FALSE unless explicitly reading archive.
--
-- SPORT: MASTER-TABLES.md → memory_facts (RAG-08)

CREATE TABLE IF NOT EXISTS memory_facts (
    id           TEXT NOT NULL PRIMARY KEY,        -- UUID v4
    namespace    TEXT NOT NULL,                    -- 'personal' | 'dev-<slug>' | 'meta'
    subject      TEXT NOT NULL,                    -- e.g. "user", "project:nself"
    predicate    TEXT NOT NULL,                    -- e.g. "prefers", "owns"
    value        TEXT NOT NULL,                    -- e.g. "dark mode"
    confidence   REAL NOT NULL DEFAULT 1.0,        -- 0.0–1.0
    content_hash TEXT NOT NULL UNIQUE,             -- BLAKE3(namespace + subject + predicate + value)
    last_seen    TEXT,                             -- ISO 8601 UTC
    archived     INTEGER NOT NULL DEFAULT 0        -- 0=active, 1=archived (decayed)
);

CREATE INDEX IF NOT EXISTS idx_mem_facts_namespace ON memory_facts (namespace, archived);
CREATE INDEX IF NOT EXISTS idx_mem_facts_subject   ON memory_facts (namespace, subject, archived);
CREATE INDEX IF NOT EXISTS idx_mem_facts_last_seen ON memory_facts (namespace, last_seen DESC);
