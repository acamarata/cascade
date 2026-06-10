//! # db::migrations
//!
//! Versioned, forward-only SQLite migration runner for the RAG schema.
//!
//! ## Strategy
//! `PRAGMA user_version` is used as the migration counter. Each migration is
//! numbered sequentially (1-based). Migrations are embedded at compile time via
//! `include_str!`. Re-running on an already-migrated DB is a no-op (idempotent).
//!
//! ## Version map
//! | user_version | migration applied              | feature gate  |
//! |-------------|--------------------------------|---------------|
//! | 0           | (initial state, no tables)    | —             |
//! | 1           | 0001_sources.sql              | always        |
//! | 2           | 0002_chunks.sql               | always        |
//! | 3           | 0003_citations.sql            | always        |
//! | 4           | 0005_fts5.sql                 | always        |
//! | 5           | 0004_embeddings.sql (vec0)    | `vec`         |
//! | 5           | 0004_embeddings_blob.sql      | no `vec`      |
//! | 6           | 0006_token_embeddings.sql     | `rag-multivec`|
//! | 7           | 0007_index_state.sql          | always        |
//! | 8           | 0008_context_fingerprints.sql | always        |
//!
//! Both paths (vec and blob) advance to `user_version=5`.
//! Migration 6 advances to `user_version=6` only when `rag-multivec` is enabled.
//! Migration 7 advances to `user_version=7` (index_state).
//! Migration 8 advances to `user_version=8` (context_fingerprints — T-P4-E04-21).
//!
//! SPORT: MASTER-TABLES.md → rag_sources, rag_chunks, rag_citations, rag_fts5,
//!        rag_embeddings, rag_sparse_embeddings, index_state, context_fingerprints

use rusqlite::{Connection, Result};

// Embedded SQL — paths are relative to the crate root (where Cargo.toml lives).
const SQL_0001: &str = include_str!("../../migrations/0001_sources.sql");
const SQL_0002: &str = include_str!("../../migrations/0002_chunks.sql");
const SQL_0003: &str = include_str!("../../migrations/0003_citations.sql");
// 0004: sqlite-vec vec0 virtual table (requires `vec` feature).
#[cfg(feature = "vec")]
const SQL_0004: &str = include_str!("../../migrations/0004_embeddings.sql");
// 0004 blob fallback: plain BLOB table, used when `vec` feature is disabled.
#[cfg(not(feature = "vec"))]
const SQL_0004_BLOB: &str = include_str!("../../migrations/0004_embeddings_blob.sql");
const SQL_0005: &str = include_str!("../../migrations/0005_fts5.sql");
// 0006: per-token ColBERT-style embedding table (requires `rag-multivec` feature).
#[cfg(feature = "rag-multivec")]
const SQL_0006: &str = include_str!("../../migrations/0006_token_embeddings.sql");
// 0007: index_state table for incremental indexing delta detection.
const SQL_0007: &str = include_str!("../../migrations/0007_index_state.sql");
// 0008: context_fingerprints table for cross-session chunk dedup (T-P4-E04-21).
const SQL_0008: &str = include_str!("../../migrations/0008_context_fingerprints.sql");

/// Apply all pending migrations to `conn`.
///
/// # Idempotency
/// Safe to call on an already-migrated database. Already-applied migrations are
/// skipped based on `PRAGMA user_version`.
///
/// # Foreign keys
/// Caller is responsible for `PRAGMA foreign_keys = ON` if FK enforcement is
/// desired at the connection level.
///
/// # Returns
/// `Ok(())` when all applicable migrations have been applied. Returns
/// `Err(rusqlite::Error)` on the first SQL failure.
pub fn run_migrations(conn: &Connection) -> Result<()> {
    let version = user_version(conn)?;

    if version < 1 {
        apply(conn, SQL_0001)?;
        set_user_version(conn, 1)?;
    }
    if version < 2 {
        apply(conn, SQL_0002)?;
        set_user_version(conn, 2)?;
    }
    if version < 3 {
        apply(conn, SQL_0003)?;
        set_user_version(conn, 3)?;
    }
    if version < 4 {
        apply(conn, SQL_0005)?; // FTS5 — always enabled
        set_user_version(conn, 4)?;
    }
    #[cfg(feature = "vec")]
    if version < 5 {
        apply(conn, SQL_0004)?; // sqlite-vec vec0 virtual table
        set_user_version(conn, 5)?;
    }
    // Without the `vec` feature, create plain BLOB rag_embeddings + rag_sparse_embeddings.
    #[cfg(not(feature = "vec"))]
    if version < 5 {
        apply(conn, SQL_0004_BLOB)?;
        set_user_version(conn, 5)?;
    }

    // Migration 6: per-token ColBERT embedding table. Only when `rag-multivec` is enabled.
    // Disk usage is ~3-4x the dense embedding table — warn users via cascade.toml docs.
    #[cfg(feature = "rag-multivec")]
    if version < 6 {
        apply(conn, SQL_0006)?;
        set_user_version(conn, 6)?;
    }

    // Migration 7: index_state for incremental-indexing delta detection.
    if version < 7 {
        apply(conn, SQL_0007)?;
        set_user_version(conn, 7)?;
    }

    // Migration 8: context_fingerprints for cross-session chunk dedup (T-P4-E04-21).
    if version < 8 {
        apply(conn, SQL_0008)?;
        set_user_version(conn, 8)?;
    }

    Ok(())
}

/// Returns the current `PRAGMA user_version` of `conn`.
fn user_version(conn: &Connection) -> Result<u32> {
    conn.query_row("PRAGMA user_version", [], |row| row.get(0))
}

/// Sets `PRAGMA user_version` to `v`. The PRAGMA does not accept bound params,
/// so we format it directly — this is safe because `v` is a Rust `u32`.
fn set_user_version(conn: &Connection, v: u32) -> Result<()> {
    conn.execute_batch(&format!("PRAGMA user_version = {v}"))
}

/// Executes a multi-statement SQL script on `conn`.
fn apply(conn: &Connection, sql: &str) -> Result<()> {
    conn.execute_batch(sql)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use rusqlite::Connection;

    /// Register sqlite-vec as an auto-extension so every subsequent connection
    /// can create vec0 virtual tables. Idempotent (SQLite deduplicates).
    #[cfg(feature = "vec")]
    fn load_vec_extension() {
        use rusqlite::ffi::sqlite3_auto_extension;
        unsafe {
            sqlite3_auto_extension(Some(std::mem::transmute(
                sqlite_vec::sqlite3_vec_init as *const (),
            )));
        }
    }

    /// Open a fresh in-memory database and run all migrations.
    fn migrated_db() -> Connection {
        #[cfg(feature = "vec")]
        load_vec_extension();

        let conn = Connection::open_in_memory().expect("in-memory DB");
        run_migrations(&conn).expect("migrations must succeed");
        conn
    }

    // --- db::migrations::fresh -----------------------------------------------

    #[test]
    fn fresh_db_migrates_to_expected_version() {
        let conn = migrated_db();
        let version: u32 = conn
            .query_row("PRAGMA user_version", [], |r| r.get(0))
            .unwrap();

        // Migrations 0007 (index_state) and 0008 (context_fingerprints) run
        // unconditionally, so every configuration lands on user_version=8.
        // (Migration 6 is rag-multivec-only; versions may skip it.)
        assert_eq!(version, 8, "expected final user_version=8");
    }

    // --- db::migrations::idempotent ------------------------------------------

    #[test]
    fn idempotent_double_run() {
        let conn = migrated_db();
        // Second call must not panic or error.
        run_migrations(&conn).expect("second run must be idempotent");
        let version: u32 = conn
            .query_row("PRAGMA user_version", [], |r| r.get(0))
            .unwrap();
        assert_eq!(version, 8, "idempotent: final user_version=8");
    }

    // --- db::migrations::tables_exist ----------------------------------------

    #[test]
    fn rag_sources_table_exists() {
        let conn = migrated_db();
        let n: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='rag_sources'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(n, 1, "rag_sources table must exist");
    }

    #[test]
    fn rag_chunks_table_exists() {
        let conn = migrated_db();
        let n: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='rag_chunks'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(n, 1, "rag_chunks table must exist");
    }

    #[test]
    fn rag_citations_table_exists() {
        let conn = migrated_db();
        let n: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='rag_citations'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(n, 1, "rag_citations table must exist");
    }

    // --- db::migrations::fts5 ------------------------------------------------

    #[test]
    fn fts5_table_and_triggers_exist() {
        let conn = migrated_db();

        // Virtual table
        let fts: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='rag_fts5'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(fts, 1, "rag_fts5 virtual table must exist");

        // All three sync triggers
        for trigger in &["rag_fts5_ai", "rag_fts5_au", "rag_fts5_bd"] {
            let t: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?1",
                    [trigger],
                    |r| r.get(0),
                )
                .unwrap();
            assert_eq!(t, 1, "trigger {trigger} must exist");
        }
    }

    #[test]
    fn fts5_insert_and_match() {
        let conn = migrated_db();

        // Insert a source row first (FK parent).
        conn.execute(
            "INSERT INTO rag_sources (file_path, schema_version) VALUES ('/test/doc.md', 1)",
            [],
        )
        .unwrap();
        let source_id: i64 = conn.last_insert_rowid();

        // Insert a chunk — trigger should populate fts5.
        conn.execute(
            "INSERT INTO rag_chunks (source_id, chunk_index, chunk_text, schema_version)
             VALUES (?1, 0, 'Rust async programming with tokio', 1)",
            [source_id],
        )
        .unwrap();

        // FTS5 MATCH query.
        let n: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM rag_fts5 WHERE chunk_text MATCH 'tokio'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(n, 1, "fts5 MATCH must return the inserted chunk");

        // Delete the chunk — bd trigger should remove it from fts5.
        conn.execute("DELETE FROM rag_chunks WHERE source_id = ?1", [source_id])
            .unwrap();

        let after_delete: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM rag_fts5 WHERE chunk_text MATCH 'tokio'",
                [],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(after_delete, 0, "fts5 must not return deleted chunk");
    }

    // --- db::migrations::vec (only compiled with vec feature) ----------------

    #[cfg(feature = "vec")]
    mod vec {
        use super::*;

        #[test]
        fn rag_embeddings_virtual_table_exists() {
            let conn = migrated_db();
            let n: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='rag_embeddings'",
                    [],
                    |r| r.get(0),
                )
                .unwrap();
            assert_eq!(n, 1, "rag_embeddings virtual table must exist");
        }

        #[test]
        fn rag_sparse_embeddings_table_exists() {
            let conn = migrated_db();
            let n: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='rag_sparse_embeddings'",
                    [],
                    |r| r.get(0),
                )
                .unwrap();
            assert_eq!(n, 1, "rag_sparse_embeddings table must exist");
        }
    }

    // --- db::migrations::schema_columns --------------------------------------

    #[test]
    fn rag_chunks_has_required_columns() {
        let conn = migrated_db();
        // PRAGMA table_info returns one row per column.
        let mut stmt = conn
            .prepare("PRAGMA table_info(rag_chunks)")
            .unwrap();
        let cols: Vec<String> = stmt
            .query_map([], |r| r.get::<_, String>(1))
            .unwrap()
            .filter_map(|r| r.ok())
            .collect();

        for required in &["id", "source_id", "chunk_index", "chunk_text", "line_start", "line_end"] {
            assert!(
                cols.contains(&required.to_string()),
                "rag_chunks must have column '{required}'"
            );
        }
    }
}
