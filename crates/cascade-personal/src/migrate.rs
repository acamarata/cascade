//! # cascade-personal::migrate
//!
//! Purpose: Schema migrations for `personal.db` via the cascade-db MigrationRegistry.
//! Inputs: a configured `rusqlite::Connection` (opened via `cascade_db::open_configured`).
//! Outputs: schema at head version — seeded collections, custom-collections, vault-meta,
//!          exposure_log tables.
//! Constraints: versions are local to this database; they do NOT share a namespace with
//!              any other Cascade database. PRAGMA user_version is the sole SOT.
//!
//! SPORT: cascade-personal / migrate

use cascade_db::{MigrationRegistry, MigrationStep};

/// Current head schema version for `personal.db`.
pub const PERSONAL_DB_SCHEMA_VERSION: u32 = 1;

/// Build the migration registry for the personal vault database.
///
/// Versions are local to personal.db — they do NOT conflict with tasks.db,
/// registry.db, or any other Cascade database's user_version.
pub fn registry() -> MigrationRegistry {
    MigrationRegistry::new(
        "personal",
        vec![MigrationStep {
            version: 1,
            name: "initial_personal_schema",
            up: |tx| {
                tx.execute_batch(
                    "
                    -- ── vault_meta ───────────────────────────────────────────────────────
                    -- One row: tracks the DEK hint stored in the keychain (never the DEK
                    -- itself) and the schema creation timestamp.
                    CREATE TABLE IF NOT EXISTS vault_meta (
                        id              INTEGER PRIMARY KEY CHECK (id = 1),
                        keychain_service TEXT    NOT NULL,
                        keychain_account TEXT    NOT NULL,
                        created_at      TEXT    NOT NULL   -- RFC 3339 UTC
                    );

                    -- ── collections ─────────────────────────────────────────────────────
                    -- Seeded well-known collections plus any user-defined ones.
                    -- `sensitivity` controls mode-gate behaviour:
                    --   'standard'   → always visible
                    --   'restricted' → hidden in non-Personal mode
                    CREATE TABLE IF NOT EXISTS collections (
                        name        TEXT NOT NULL PRIMARY KEY,
                        label       TEXT NOT NULL,
                        sensitivity TEXT NOT NULL DEFAULT 'standard'
                                    CHECK (sensitivity IN ('standard', 'restricted'))
                    );

                    -- Seed well-known collections.
                    INSERT OR IGNORE INTO collections (name, label, sensitivity) VALUES
                        ('people',       'People & Contacts',   'standard'),
                        ('family',       'Family',              'standard'),
                        ('finance',      'Finance',             'restricted'),
                        ('health',       'Health & Medical',    'restricted'),
                        ('records',      'Records & Documents', 'standard'),
                        ('credentials',  'Credential Metadata', 'restricted');

                    -- ── records ─────────────────────────────────────────────────────────
                    -- One row per item. `ciphertext` is the AES-256-GCM encrypted payload
                    -- (nonce prepended, 12 bytes || ciphertext). Plaintext NEVER stored.
                    -- `content_hash` is a BLAKE3 hex digest of the plaintext for
                    -- deduplication; it is computed before encryption, stored as-is (hash
                    -- leaks no payload content).
                    CREATE TABLE IF NOT EXISTS records (
                        id              TEXT NOT NULL PRIMARY KEY,  -- UUID v4
                        collection      TEXT NOT NULL
                                        REFERENCES collections(name) ON DELETE CASCADE,
                        ciphertext      BLOB NOT NULL,              -- nonce(12) || ct
                        content_hash    TEXT NOT NULL,              -- BLAKE3 hex
                        created_at      TEXT NOT NULL,              -- RFC 3339 UTC
                        updated_at      TEXT NOT NULL               -- RFC 3339 UTC
                    );

                    CREATE INDEX IF NOT EXISTS idx_records_collection
                        ON records (collection, updated_at DESC);

                    -- ── exposure_log ─────────────────────────────────────────────────────
                    -- Append-only log: every time an item is exposed to CC/MCP/cloud,
                    -- a row is inserted here (item_id may be NULL for collection-level
                    -- consent events).
                    CREATE TABLE IF NOT EXISTS exposure_log (
                        id              INTEGER PRIMARY KEY AUTOINCREMENT,
                        collection      TEXT    NOT NULL,
                        item_id         TEXT,                       -- NULL = collection-level
                        exposed_to      TEXT    NOT NULL,           -- 'cc' | 'mcp' | 'user' | other
                        granted_by      TEXT    NOT NULL DEFAULT 'user',
                        exposed_at      TEXT    NOT NULL            -- RFC 3339 UTC
                    );

                    CREATE INDEX IF NOT EXISTS idx_exposure_log_item
                        ON exposure_log (collection, item_id, exposed_at DESC);
                    ",
                )
            },
        }],
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_db::open_in_memory;

    #[test]
    fn migration_runs_and_is_idempotent() {
        let mut conn = open_in_memory().unwrap();
        let reg = registry();
        // First run applies v1.
        reg.run(&mut conn, None).unwrap();
        let v: u32 = conn
            .pragma_query_value(None, "user_version", |r| r.get(0))
            .unwrap();
        assert_eq!(v, PERSONAL_DB_SCHEMA_VERSION);
        // Second run is a no-op.
        reg.run(&mut conn, None).unwrap();
    }

    #[test]
    fn seed_collections_present() {
        let mut conn = open_in_memory().unwrap();
        registry().run(&mut conn, None).unwrap();
        let count: i64 = conn
            .query_row("SELECT COUNT(*) FROM collections", [], |r| r.get(0))
            .unwrap();
        assert_eq!(count, 6, "expected 6 seeded collections");
    }

    #[test]
    fn restricted_collections_seeded_correctly() {
        let mut conn = open_in_memory().unwrap();
        registry().run(&mut conn, None).unwrap();
        let mut stmt = conn
            .prepare("SELECT name FROM collections WHERE sensitivity='restricted' ORDER BY name")
            .unwrap();
        let names: Vec<String> = stmt
            .query_map([], |r| r.get(0))
            .unwrap()
            .filter_map(|r| r.ok())
            .collect();
        assert_eq!(names, vec!["credentials", "finance", "health"]);
    }
}
