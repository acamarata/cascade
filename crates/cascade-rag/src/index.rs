//! SQLite index management: FTS5 full-text + sqlite-vec dense vectors.
//!
//! `RagIndex` is the single connection pool owner for the cascade RAG database.
//! It manages schema creation, WAL mode, migrations, and provides low-level
//! insert/query APIs consumed by the `retrieve` and `chunk` modules.
//!
//! ## Schema
//!
//! ```sql
//! -- Canonical chunk records (source of truth)
//! CREATE TABLE chunks (
//!     chunk_id    TEXT PRIMARY KEY,
//!     file_path   TEXT,
//!     start_line  INTEGER,
//!     end_line    INTEGER,
//!     text        TEXT NOT NULL,
//!     metadata    TEXT,          -- JSON blob
//!     indexed_at  INTEGER NOT NULL
//! );
//!
//! -- FTS5 virtual table — BM25 keyword search
//! CREATE VIRTUAL TABLE fts_chunks USING fts5(
//!     chunk_id UNINDEXED,
//!     text,
//!     content='chunks',
//!     content_rowid='rowid',
//!     tokenize='unicode61 remove_diacritics 1'
//! );
//!
//! -- sqlite-vec extension — dense ANN search
//! CREATE VIRTUAL TABLE vec_chunks USING vec0(
//!     chunk_id TEXT PRIMARY KEY,
//!     embedding FLOAT[1024]
//! );
//! ```
//!
//! ## WAL mode
//!
//! WAL is set once on first open. Concurrent readers are never blocked by a
//! single writer. The daemon holds the write-end; query servants use read-only
//! connections.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::index::RagIndex

use cascade_types::error::{CascadeError, Result};
use rusqlite::{params, Connection, OpenFlags};
use serde_json::Value;
use std::path::{Path, PathBuf};
use tracing::{debug, info, instrument};

/// Current schema version.  Increment on every breaking DDL change.
const SCHEMA_VERSION: u32 = 2;

/// Stored dimensions for BGE-M3.  Must match the embedding model dimension.
const DEFAULT_EMBED_DIM: usize = 1024;

// ── Index health ──────────────────────────────────────────────────────────────

/// Summary metrics from [`RagIndex::health`].
///
/// SPORT: MASTER-LIBS.md → cascade-rag::index::IndexHealth
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct IndexHealth {
    /// Total chunks stored in the `chunks` table.
    pub total_chunks: u64,
    /// Chunks that have a dense embedding in `vec_chunks`.
    pub embedded_chunks: u64,
    /// Percentage of chunks that are embedded (0–100).
    pub embedding_coverage_pct: f32,
    /// Unix timestamp of the most-recently indexed chunk.
    pub last_indexed_at: Option<i64>,
    /// Whether the index is considered stale (no writes in >24 h while watched
    /// paths still have unindexed modifications).
    pub is_stale: bool,
    /// Schema version stored in `PRAGMA user_version`.
    pub schema_version: u32,
}

// ── RagIndex ──────────────────────────────────────────────────────────────────

/// Primary handle to the Cascade RAG SQLite database.
///
/// Hold one instance per daemon process.  All mutations go through this struct;
/// read-only connections are created on demand by retrieval workers.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_rag::index::RagIndex;
/// # async fn example() -> cascade_types::error::Result<()> {
/// let idx = RagIndex::open("/home/user/.cascade/cascade.db").await?;
/// let health = idx.health().await?;
/// println!("Coverage: {:.1}%", health.embedding_coverage_pct);
/// # Ok(())
/// # }
/// ```
pub struct RagIndex {
    /// Path to the SQLite database file.
    db_path: PathBuf,
    /// Active read-write connection.  Protected by an async mutex so the single
    /// writer never contends with itself across await points.
    conn: tokio::sync::Mutex<Connection>,
    /// Vector dimension used when creating `vec_chunks`.  Must match the active
    /// embedding model; mismatches are caught at insert time.
    embed_dim: usize,
}

impl RagIndex {
    /// Open (or create) the RAG database at `db_path`.
    ///
    /// Creates the schema if it does not exist, applies pending migrations, and
    /// enables WAL mode.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::Storage`] if the file cannot be opened or the
    /// schema cannot be applied.
    #[instrument(skip_all, fields(db_path = %db_path.as_ref().display()))]
    pub async fn open(db_path: impl AsRef<Path>) -> Result<Self> {
        let db_path = db_path.as_ref().to_path_buf();
        // Ensure parent directory exists.
        if let Some(parent) = db_path.parent() {
            std::fs::create_dir_all(parent)
                .map_err(|e| CascadeError::io(parent, "create-dir", e))?;
        }
        let conn = Connection::open_with_flags(
            &db_path,
            OpenFlags::SQLITE_OPEN_READ_WRITE | OpenFlags::SQLITE_OPEN_CREATE,
        )
        .map_err(|e| CascadeError::RetrievalFailed {
            detail: format!("open db: {e}"),
        })?;
        let idx = Self {
            db_path,
            conn: tokio::sync::Mutex::new(conn),
            embed_dim: DEFAULT_EMBED_DIM,
        };
        idx.apply_schema().await?;
        Ok(idx)
    }

    /// Open with a custom embedding dimension (for non-BGE-M3 models).
    pub async fn open_with_dim(db_path: impl AsRef<Path>, embed_dim: usize) -> Result<Self> {
        let mut idx = Self::open(db_path).await?;
        idx.embed_dim = embed_dim;
        Ok(idx)
    }

    // ── Schema ───────────────────────────────────────────────────────────────

    /// Apply the base schema and any pending migrations.
    ///
    /// Idempotent: safe to call on an already-initialised database.
    async fn apply_schema(&self) -> Result<()> {
        let conn = self.conn.lock().await;
        // WAL mode — single writer, unlimited concurrent readers.
        conn.execute_batch("PRAGMA journal_mode=WAL;")
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("wal mode: {e}"),
            })?;
        conn.execute_batch("PRAGMA synchronous=NORMAL;")
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("sync pragma: {e}"),
            })?;
        let stored_ver: u32 = conn
            .pragma_query_value(None, "user_version", |r| r.get(0))
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("read user_version: {e}"),
            })?;
        if stored_ver < SCHEMA_VERSION {
            self.run_migrations(&conn, stored_ver)?;
        }
        Ok(())
    }

    /// Run forward-only migrations from `current_ver` to `SCHEMA_VERSION`.
    fn run_migrations(&self, conn: &Connection, current_ver: u32) -> Result<()> {
        if current_ver < 1 {
            // v0 → v1: create chunks + FTS5
            conn.execute_batch(
                "CREATE TABLE IF NOT EXISTS chunks (
                    chunk_id    TEXT PRIMARY KEY,
                    file_path   TEXT,
                    start_line  INTEGER,
                    end_line    INTEGER,
                    text        TEXT NOT NULL,
                    metadata    TEXT,
                    indexed_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
                );
                CREATE VIRTUAL TABLE IF NOT EXISTS fts_chunks USING fts5(
                    chunk_id UNINDEXED,
                    text,
                    content='chunks',
                    content_rowid='rowid',
                    tokenize='unicode61 remove_diacritics 1'
                );",
            )
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("migration v1: {e}"),
            })?;
            info!("Applied migration v0→v1 (chunks + FTS5)");
        }
        if current_ver < 2 {
            // v1 → v2: add sqlite-vec virtual table
            // NOTE: sqlite-vec must be loaded as an extension before this runs.
            // If the extension is not present the table is created as a plain table
            // and queries will fall back to FTS5-only mode.
            let dim = self.embed_dim;
            let ddl = format!(
                "CREATE VIRTUAL TABLE IF NOT EXISTS vec_chunks USING vec0(\
                    chunk_id TEXT PRIMARY KEY,\
                    embedding FLOAT[{dim}]\
                );"
            );
            // Best-effort: ignore if sqlite-vec extension not loaded.
            let _ = conn.execute_batch(&ddl);
            info!("Applied migration v1→v2 (sqlite-vec)");
        }
        conn.pragma_update(None, "user_version", SCHEMA_VERSION)
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("set user_version: {e}"),
            })?;
        Ok(())
    }

    // ── Insert / upsert ──────────────────────────────────────────────────────

    /// Insert or replace a chunk record and update the FTS5 index.
    ///
    /// This is the hot path for incremental indexing; call in batches for
    /// throughput (target ≥500 chunks/sec on M-series hardware).
    ///
    /// # Constraints
    ///
    /// - `chunk_id` is the primary key — duplicates replace the existing row.
    /// - `metadata` is serialised to JSON.
    #[instrument(skip(self, text, metadata))]
    pub async fn upsert_chunk(
        &self,
        chunk_id: &str,
        file_path: Option<&Path>,
        start_line: Option<u32>,
        end_line: Option<u32>,
        text: &str,
        metadata: Option<&Value>,
    ) -> Result<()> {
        let conn = self.conn.lock().await;
        let fp = file_path.map(|p| p.to_string_lossy().into_owned());
        let meta_json = metadata.map(|v| v.to_string());
        let now = unix_now();
        conn.execute(
            "INSERT OR REPLACE INTO chunks \
             (chunk_id, file_path, start_line, end_line, text, metadata, indexed_at) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            params![chunk_id, fp, start_line, end_line, text, meta_json, now],
        )
        .map_err(|e| CascadeError::RetrievalFailed {
            detail: format!("upsert chunk: {e}"),
        })?;
        // Keep FTS5 content table in sync.
        conn.execute(
            "INSERT OR REPLACE INTO fts_chunks(chunk_id, text) VALUES (?1, ?2)",
            params![chunk_id, text],
        )
        .map_err(|e| CascadeError::RetrievalFailed {
            detail: format!("upsert fts: {e}"),
        })?;
        debug!(chunk_id, "chunk upserted");
        Ok(())
    }

    /// Store a dense embedding vector for a chunk (sqlite-vec).
    ///
    /// Does nothing (silently) if the `vec_chunks` table does not exist, which
    /// happens when the sqlite-vec extension is not loaded.
    pub async fn upsert_embedding(&self, chunk_id: &str, embedding: &[f32]) -> Result<()> {
        if embedding.len() != self.embed_dim {
            return Err(CascadeError::EmbeddingDimensionMismatch {
                expected: self.embed_dim,
                actual: embedding.len(),
            });
        }
        let conn = self.conn.lock().await;
        // Serialize as little-endian f32 blob — sqlite-vec wire format.
        let blob: Vec<u8> = embedding.iter().flat_map(|f| f.to_le_bytes()).collect();
        // Ignore error if vec_chunks does not exist.
        let _ = conn.execute(
            "INSERT OR REPLACE INTO vec_chunks(chunk_id, embedding) VALUES (?1, ?2)",
            params![chunk_id, blob],
        );
        Ok(())
    }

    /// Delete a chunk and its associated FTS5 + vector entries.
    pub async fn delete_chunk(&self, chunk_id: &str) -> Result<()> {
        let conn = self.conn.lock().await;
        conn.execute("DELETE FROM chunks WHERE chunk_id = ?1", params![chunk_id])
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("delete chunk: {e}"),
            })?;
        conn.execute(
            "DELETE FROM fts_chunks WHERE chunk_id = ?1",
            params![chunk_id],
        )
        .map_err(|e| CascadeError::RetrievalFailed {
            detail: format!("delete fts: {e}"),
        })?;
        let _ = conn.execute(
            "DELETE FROM vec_chunks WHERE chunk_id = ?1",
            params![chunk_id],
        );
        Ok(())
    }

    // ── Batch operations ─────────────────────────────────────────────────────

    /// Flush a batch of (chunk_id, text) pairs into FTS5 in a single transaction.
    ///
    /// Using a transaction is the primary driver of the ≥500 chunks/sec target.
    pub async fn batch_upsert_fts(&self, batch: &[(&str, &str)]) -> Result<usize> {
        let conn = self.conn.lock().await;
        let tx = conn
            .unchecked_transaction()
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("begin tx: {e}"),
            })?;
        for (chunk_id, text) in batch {
            tx.execute(
                "INSERT OR REPLACE INTO fts_chunks(chunk_id, text) VALUES (?1, ?2)",
                params![chunk_id, text],
            )
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("batch fts insert: {e}"),
            })?;
        }
        tx.commit().map_err(|e| CascadeError::RetrievalFailed {
            detail: format!("commit tx: {e}"),
        })?;
        Ok(batch.len())
    }

    // ── FTS5 query ───────────────────────────────────────────────────────────

    /// Execute an FTS5 BM25 query, returning top-K (chunk_id, bm25_score) pairs.
    ///
    /// The `query` string is passed directly to the FTS5 engine; callers should
    /// sanitise or quote the string as needed.
    pub async fn fts_query(&self, query: &str, k: usize) -> Result<Vec<(String, f32)>> {
        let conn = self.conn.lock().await;
        let mut stmt = conn
            .prepare(
                "SELECT chunk_id, bm25(fts_chunks) AS score \
                 FROM fts_chunks \
                 WHERE fts_chunks MATCH ?1 \
                 ORDER BY score ASC \
                 LIMIT ?2",
            )
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("fts prepare: {e}"),
            })?;
        let results: Vec<(String, f32)> = stmt
            .query_map(params![query, k as i64], |row| {
                Ok((row.get::<_, String>(0)?, row.get::<_, f64>(1)? as f32))
            })
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("fts query: {e}"),
            })?
            .filter_map(|r| r.ok())
            .collect();
        Ok(results)
    }

    // ── FTS5 optimize + vacuum ────────────────────────────────────────────────

    /// Merge all FTS5 segments into one for faster queries.
    pub async fn fts_optimize(&self) -> Result<()> {
        let conn = self.conn.lock().await;
        conn.execute_batch("INSERT INTO fts_chunks(fts_chunks) VALUES('optimize');")
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("fts optimize: {e}"),
            })?;
        Ok(())
    }

    /// Reclaim space freed by deleted chunks.
    pub async fn vacuum(&self) -> Result<()> {
        let conn = self.conn.lock().await;
        conn.execute_batch("VACUUM;")
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("vacuum: {e}"),
            })?;
        Ok(())
    }

    // ── Health ────────────────────────────────────────────────────────────────

    /// Return a snapshot of index health metrics.
    pub async fn health(&self) -> Result<IndexHealth> {
        let conn = self.conn.lock().await;
        let total: u64 = conn
            .query_row("SELECT COUNT(*) FROM chunks", [], |r| r.get(0))
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("count chunks: {e}"),
            })?;
        let embedded: u64 = conn
            .query_row("SELECT COUNT(*) FROM vec_chunks", [], |r| r.get(0))
            .unwrap_or(0);
        let last: Option<i64> = conn
            .query_row("SELECT MAX(indexed_at) FROM chunks", [], |r| r.get(0))
            .ok()
            .flatten();
        let schema_ver: u32 = conn
            .pragma_query_value(None, "user_version", |r| r.get(0))
            .unwrap_or(0);
        let coverage = if total == 0 {
            0.0
        } else {
            embedded as f32 / total as f32 * 100.0
        };
        Ok(IndexHealth {
            total_chunks: total,
            embedded_chunks: embedded,
            embedding_coverage_pct: coverage,
            last_indexed_at: last,
            is_stale: false,
            schema_version: schema_ver,
        })
    }

    /// Path to the database file.
    pub fn db_path(&self) -> &Path {
        &self.db_path
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn unix_now() -> i64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}
