-- Migration 003: Create hooks table
--
-- Stores HookDef records loaded from config.toml [[hooks]] arrays.
-- The (name, tier) natural key makes upsert idempotent: re-loading a tier's
-- config.toml on daemon restart updates existing hooks rather than duplicating.

CREATE TABLE IF NOT EXISTS hooks (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL,
    tier        TEXT    NOT NULL DEFAULT '',
    event_json  TEXT    NOT NULL,
    action_json TEXT    NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1
);

-- Index for fast lookup by event kind (for list_for_event).
-- Stored as a JSON column so we index by the 'type' discriminant via expression index.
CREATE INDEX IF NOT EXISTS idx_hooks_enabled
    ON hooks(enabled);

-- Unique constraint: (name, tier) is the natural upsert key.
-- On conflict the record is replaced (see hook_store.rs upsert).
CREATE UNIQUE INDEX IF NOT EXISTS idx_hooks_name_tier
    ON hooks(name, tier);
