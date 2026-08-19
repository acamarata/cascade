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
//! | 9           | 0009_chunk_meta.sql           | always        |
//! | 10          | 0010_memory_episodes.sql      | always        |
//! | 11          | 0011_memory_facts.sql         | always        |
//! | 12          | 0012_chat_history.sql         | always        |
//! | 13          | DiskANN reconciliation        | `vec`         |
//!
//! Both paths (vec and blob) advance to `user_version=5`.
//! Migration 6 advances to `user_version=6` only when `rag-multivec` is enabled.
//! Migration 7 advances to `user_version=7` (index_state).
//! Migration 8 advances to `user_version=8` (context_fingerprints — T-P4-E04-21).
//! Migration 9 advances to `user_version=9` (curated-description metadata table).
//! Migrations 10–12 (RAG-08): memory_episodes, memory_facts, chat_history.
//! Migration 13 upgrades either a plain-BLOB or flat vec0 `rag_embeddings`
//! table in place to the sqlite-vec DiskANN schema. The reconciliation also
//! runs on already-versioned databases so switching from a non-`vec` build is
//! safe.
//!
//! SPORT: MASTER-TABLES.md → rag_sources, rag_chunks, rag_citations, rag_fts5,
//!        rag_embeddings, rag_sparse_embeddings, index_state, context_fingerprints,
//!        memory_episodes, memory_facts, chat_history

#[cfg(feature = "vec")]
use rusqlite::OptionalExtension;
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
// 0009: curated document-level metadata for the description+title+tags retrieval channel.
const SQL_0009: &str = include_str!("../../migrations/0009_chunk_meta.sql");
// 0010–0012: RAG-08 memory module tables.
const SQL_0010: &str = include_str!("../../migrations/0010_memory_episodes.sql");
const SQL_0011: &str = include_str!("../../migrations/0011_memory_facts.sql");
const SQL_0012: &str = include_str!("../../migrations/0012_chat_history.sql");

const LATEST_SCHEMA_VERSION: u32 = 13;

#[cfg(feature = "vec")]
const DISKANN_EMBEDDINGS_DDL: &str = "CREATE VIRTUAL TABLE {table} USING vec0 (
    rowid INTEGER PRIMARY KEY,
    embedding float[1024] distance_metric=cosine indexed by diskann(
        neighbor_quantizer=int8,
        n_neighbors=64,
        search_list_size_insert=128,
        search_list_size_search=256
    )
)";

/// Register sqlite-vec for every SQLite connection opened after this call.
///
/// Registration is process-global and idempotent. Keeping it here makes the
/// production migration/open path use the same statically-linked extension as
/// tests and embedding storage.
#[cfg(feature = "vec")]
pub fn register_sqlite_vec() {
    cascade_db::vector::register_sqlite_vec();
}

/// No-op for builds that deliberately disable dense sqlite-vec storage.
#[cfg(not(feature = "vec"))]
pub fn register_sqlite_vec() {}

#[cfg(feature = "vec")]
fn load_sqlite_vec(conn: &Connection) -> Result<()> {
    let mut error = std::ptr::null();
    // SAFETY: the connection handle is valid for the lifetime of `conn`; the
    // extension was compiled with SQLITE_CORE and therefore does not dereference
    // the null extension API pointer.
    let code = unsafe { sqlite_vec::sqlite3_vec_init(conn.handle(), &mut error, std::ptr::null()) };
    if code == rusqlite::ffi::SQLITE_OK {
        return Ok(());
    }

    let detail = if error.is_null() {
        None
    } else {
        // SAFETY: sqlite-vec returns an SQLite-allocated, NUL-terminated error.
        let message = unsafe { std::ffi::CStr::from_ptr(error) }
            .to_string_lossy()
            .into_owned();
        unsafe { rusqlite::ffi::sqlite3_free(error.cast_mut().cast()) };
        Some(message)
    };
    Err(rusqlite::Error::SqliteFailure(
        rusqlite::ffi::Error::new(code),
        detail,
    ))
}

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
    register_sqlite_vec();
    #[cfg(feature = "vec")]
    load_sqlite_vec(conn)?;
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

    // Migration 9: curated document-level metadata (description/title/tags FTS channel).
    if version < 9 {
        apply(conn, SQL_0009)?;
        set_user_version(conn, 9)?;
    }

    // Migration 10: memory_episodes — episodic memory scoped by namespace (RAG-08).
    if version < 10 {
        apply(conn, SQL_0010)?;
        set_user_version(conn, 10)?;
    }

    // Migration 11: memory_facts — consolidated factual memory with BLAKE3 dedup (RAG-08).
    if version < 11 {
        apply(conn, SQL_0011)?;
        set_user_version(conn, 11)?;
    }

    // Migration 12: chat_history — per-scope, per-namespace chat message log (RAG-08).
    if version < 12 {
        apply(conn, SQL_0012)?;
        set_user_version(conn, 12)?;
    }

    #[cfg(feature = "vec")]
    ensure_diskann_embeddings(conn)?;

    if version < LATEST_SCHEMA_VERSION {
        set_user_version(conn, LATEST_SCHEMA_VERSION)?;
    }

    Ok(())
}

#[cfg(feature = "vec")]
fn ensure_diskann_embeddings(conn: &Connection) -> Result<()> {
    let schema: Option<String> = conn
        .query_row(
            "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'rag_embeddings'",
            [],
            |row| row.get(0),
        )
        .optional()?;

    if schema
        .as_deref()
        .is_some_and(|sql| sql.to_ascii_lowercase().contains("indexed by diskann"))
    {
        return Ok(());
    }

    if schema.is_none() {
        conn.execute_batch(&DISKANN_EMBEDDINGS_DDL.replace("{table}", "rag_embeddings"))?;
        return Ok(());
    }

    conn.execute_batch("BEGIN IMMEDIATE")?;
    let migration = (|| {
        let old_count: i64 =
            conn.query_row("SELECT COUNT(*) FROM rag_embeddings", [], |row| row.get(0))?;
        conn.execute_batch(
            "DROP TABLE IF EXISTS temp.rag_embeddings_ann_migration;
             CREATE TEMP TABLE rag_embeddings_ann_migration (
                 rowid INTEGER PRIMARY KEY,
                 embedding BLOB NOT NULL
             );
             INSERT INTO rag_embeddings_ann_migration(rowid, embedding)
             SELECT rowid, embedding FROM rag_embeddings ORDER BY rowid;",
        )?;
        let staged_count: i64 = conn.query_row(
            "SELECT COUNT(*) FROM rag_embeddings_ann_migration",
            [],
            |row| row.get(0),
        )?;
        if old_count != staged_count {
            return Err(row_count_mismatch(old_count, staged_count));
        }

        conn.execute_batch("DROP TABLE rag_embeddings")?;
        conn.execute_batch(&DISKANN_EMBEDDINGS_DDL.replace("{table}", "rag_embeddings"))?;
        conn.execute(
            "INSERT INTO rag_embeddings(rowid, embedding) \
             SELECT rowid, embedding FROM rag_embeddings_ann_migration ORDER BY rowid",
            [],
        )?;
        let new_count: i64 =
            conn.query_row("SELECT COUNT(*) FROM rag_embeddings", [], |row| row.get(0))?;
        if old_count != new_count {
            return Err(row_count_mismatch(old_count, new_count));
        }

        conn.execute_batch("DROP TABLE temp.rag_embeddings_ann_migration")?;
        Ok(())
    })();

    match migration {
        Ok(()) => conn.execute_batch("COMMIT"),
        Err(error) => {
            let _ = conn.execute_batch("ROLLBACK");
            Err(error)
        }
    }
}

#[cfg(feature = "vec")]
fn row_count_mismatch(source: i64, destination: i64) -> rusqlite::Error {
    rusqlite::Error::SqliteFailure(
        rusqlite::ffi::Error::new(rusqlite::ffi::SQLITE_CONSTRAINT),
        Some(format!(
            "DiskANN migration row-count mismatch: source={source}, destination={destination}"
        )),
    )
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
        register_sqlite_vec();
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

        // Migrations 10 (memory_episodes), 11 (memory_facts), 12 (chat_history),
        // and the DiskANN schema marker run unconditionally, so every
        // configuration lands on user_version=13.
        // (Migration 6 is rag-multivec-only; versions may skip it.)
        assert_eq!(version, 13, "expected final user_version=13");
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
        assert_eq!(version, 13, "idempotent: final user_version=13");
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

        fn embedding(seed: f32) -> Vec<u8> {
            let values: Vec<f32> = (0..1024)
                .map(|index| (seed + index as f32 * 0.013).sin())
                .collect();
            values
                .iter()
                .flat_map(|value| value.to_le_bytes())
                .collect()
        }

        fn assert_diskann_schema(conn: &Connection) {
            let schema: String = conn
                .query_row(
                    "SELECT sql FROM sqlite_master WHERE name = 'rag_embeddings'",
                    [],
                    |row| row.get(0),
                )
                .unwrap();
            assert!(
                schema.to_ascii_lowercase().contains("indexed by diskann"),
                "rag_embeddings must declare a DiskANN index: {schema}"
            );

            let diskann_nodes: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM sqlite_master \
                     WHERE type = 'table' AND name = 'rag_embeddings_diskann_nodes00'",
                    [],
                    |row| row.get(0),
                )
                .unwrap();
            assert_eq!(diskann_nodes, 1, "DiskANN node index must exist");

            let flat_chunks: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM sqlite_master \
                     WHERE type = 'table' AND name = 'rag_embeddings_vector_chunks00'",
                    [],
                    |row| row.get(0),
                )
                .unwrap();
            assert_eq!(flat_chunks, 0, "flat vec0 storage must not be used");
        }

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
        fn rag_embeddings_uses_diskann_index() {
            let conn = migrated_db();
            assert_diskann_schema(&conn);
        }

        #[test]
        fn dense_knn_query_uses_vec0_knn_plan() {
            let conn = migrated_db();
            let vector = embedding(0.25);
            conn.execute(
                "INSERT INTO rag_embeddings(rowid, embedding) VALUES (?1, ?2)",
                rusqlite::params![7_i64, vector],
            )
            .unwrap();

            let mut plan = conn
                .prepare(
                    "EXPLAIN QUERY PLAN \
                     SELECT rowid, distance FROM rag_embeddings \
                     WHERE embedding MATCH ?1 AND k = ?2 ORDER BY distance",
                )
                .unwrap();
            let details: Vec<String> = plan
                .query_map(rusqlite::params![embedding(0.25), 1_i64], |row| row.get(3))
                .unwrap()
                .collect::<std::result::Result<_, _>>()
                .unwrap();

            assert!(
                details
                    .iter()
                    .any(|detail| { detail.contains("rag_embeddings VIRTUAL TABLE INDEX 0:3") }),
                "MATCH query must select sqlite-vec's KNN plan (3), got {details:?}"
            );
            assert!(
                details
                    .iter()
                    .all(|detail| { !detail.contains("rag_embeddings VIRTUAL TABLE INDEX 0:1") }),
                "query must not select sqlite-vec's full-scan plan (1): {details:?}"
            );
        }

        #[test]
        fn existing_blob_embeddings_migrate_without_data_loss() {
            let conn = Connection::open_in_memory().unwrap();
            conn.execute_batch(
                "CREATE TABLE rag_embeddings (
                    rowid INTEGER PRIMARY KEY,
                    embedding BLOB NOT NULL
                 );
                 PRAGMA user_version = 13;",
            )
            .unwrap();
            conn.execute(
                "INSERT INTO rag_embeddings(rowid, embedding) VALUES (?1, ?2)",
                rusqlite::params![41_i64, embedding(0.5)],
            )
            .unwrap();

            run_migrations(&conn).expect("legacy BLOB migration must succeed");
            assert_diskann_schema(&conn);
            let ids: Vec<i64> = conn
                .prepare(
                    "SELECT rowid FROM rag_embeddings \
                     WHERE embedding MATCH ?1 AND k = 1 ORDER BY distance",
                )
                .unwrap()
                .query_map([embedding(0.5)], |row| row.get(0))
                .unwrap()
                .collect::<std::result::Result<_, _>>()
                .unwrap();
            assert_eq!(ids, vec![41], "the legacy vector must remain queryable");
        }

        #[test]
        fn existing_flat_vec0_embeddings_migrate_without_data_loss() {
            register_sqlite_vec();
            let conn = Connection::open_in_memory().unwrap();
            conn.execute_batch(
                "CREATE VIRTUAL TABLE rag_embeddings USING vec0 (
                    rowid INTEGER PRIMARY KEY,
                    embedding float[1024]
                 );
                 PRAGMA user_version = 12;",
            )
            .unwrap();
            conn.execute(
                "INSERT INTO rag_embeddings(rowid, embedding) VALUES (?1, ?2)",
                rusqlite::params![73_i64, embedding(0.75)],
            )
            .unwrap();

            run_migrations(&conn).expect("legacy flat-vec0 migration must succeed");
            assert_diskann_schema(&conn);
            let count: i64 = conn
                .query_row("SELECT COUNT(*) FROM rag_embeddings", [], |row| row.get(0))
                .unwrap();
            assert_eq!(count, 1, "the legacy vec0 row must be preserved");
            let indexed: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM rag_embeddings_diskann_nodes00",
                    [],
                    |row| row.get(0),
                )
                .unwrap();
            assert_eq!(indexed, 1, "the migrated vector must be in DiskANN");
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
        let mut stmt = conn.prepare("PRAGMA table_info(rag_chunks)").unwrap();
        let cols: Vec<String> = stmt
            .query_map([], |r| r.get::<_, String>(1))
            .unwrap()
            .filter_map(|r| r.ok())
            .collect();

        for required in &[
            "id",
            "source_id",
            "chunk_index",
            "chunk_text",
            "line_start",
            "line_end",
        ] {
            assert!(
                cols.contains(&required.to_string()),
                "rag_chunks must have column '{required}'"
            );
        }
    }
}
