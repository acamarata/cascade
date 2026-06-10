-- Migration 0008: context_fingerprints
-- Cross-session chunk deduplication store (T-P4-E04-21).
-- Records the hash of every chunk delivered to a harness session so that
-- the same content is not re-served within a 30-minute window.
--
-- Columns:
--   session_id   — opaque harness session identifier (string)
--   chunk_hash   — SHA-256 hex of normalised chunk text
--   delivered_at — Unix seconds when this chunk was last delivered
--
-- TTL: rows older than 2 hours are purged by ContextOptimizer::cleanup_expired.
CREATE TABLE IF NOT EXISTS context_fingerprints (
    session_id   TEXT    NOT NULL,
    chunk_hash   TEXT    NOT NULL,
    delivered_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    PRIMARY KEY (session_id, chunk_hash)
);

-- Index for efficient TTL cleanup (DELETE WHERE delivered_at < cutoff).
CREATE INDEX IF NOT EXISTS idx_cfp_delivered ON context_fingerprints (delivered_at);
